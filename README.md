# Real-Time Pricing Engine

A Go service that keeps one connection to the livefxhub price feed, cleans the ticks, computes its own rolling 24h high / low / % change per symbol, and streams the result to its own WebSocket clients. Only logged-in, KYC-verified users get prices.

Go 1.27, gorilla/websocket, MongoDB, Docker.

## Running with Docker

Everything runs in Docker, including MongoDB. Nothing else needs to be installed.

**1. Create `.env`**

```bash
cp .env.example .env        # Windows: copy .env.example .env
```

Fill in:

| Variable | Value |
|---|---|
| `FEED_API_KEY`, `FEED_SECRET_KEY` | your livefxhub credentials |
| `JWT_SECRET` | any random string, at least 32 characters |
| `KYC_ENC_KEY` | exactly 64 hex characters (`openssl rand -hex 32`) |
| `ADMIN_EMAIL`, `ADMIN_PASSWORD` | the admin account created on first start |

Leave `MONGODB_URI` as it is. Docker sets it for the app.

**2. Start**

```bash
docker compose up --build
```

This starts two containers on a private Docker network:

- `mongo`: MongoDB 8. Its data is stored in the `mongo-data` volume, so it survives restarts.
- `app`: the pricing engine. It starts once Mongo passes its health check, and connects to it at `mongodb://mongo:27017` inside the Docker network.

You should see `restored N symbols from MongoDB` and then `auth response: ... "success":true`.

**3. Open** http://localhost:8080

**Useful commands**

```bash
docker compose ps                # both should be "healthy"
docker compose logs -f app       # follow the app logs
docker compose stop              # stop cleanly (closes the feed session, saves state)
docker compose down              # remove containers, keep the data
docker compose down -v           # remove containers AND the database
```

MongoDB is also published on `127.0.0.1:27017`, so you can look at the data with mongosh or Compass from the same machine. For local development: `docker compose up -d mongo`, then `go run .`.

Only run one copy of the app at a time. livefxhub allows one session per API key.

## Trying it

The page at http://localhost:8080 walks through the flow: register → connecting to prices is refused → submit KYC → log in as admin and approve → connect again and prices stream in. Each row shows the message size and latency.

WebSocket protocol (`ws://localhost:8080/ws?token=<jwt>`):

```json
→ {"action":"subscribe","symbols":["XAUUSD","EURUSD"]}
→ {"action":"unsubscribe","symbols":["EURUSD"]}
← {"s":"XAUUSD","b":4155.875,"a":4156.155,"h":4162.245,"l":4111.42,"c":1.04,"t":1792070251654}
```

`s` symbol, `b`/`a` bid/ask, `h`/`l`/`c` our own 24h high, low and % change, `t` when the server received the tick (unix ms).

## Architecture

```
livefxhub ──► feed ──► store ──► hub ──► WebSocket clients (KYC-verified only)
                         │                ▲
                         │ every 5s       │ auth: JWT + KYC check
                         ▼                │
                      persist ──► MongoDB (pricing.symbol_state, kyc.users)
```

| Package | Job |
|---|---|
| `feed/client.go` | the connection to livefxhub |
| `store/` | latest price, duplicate and ordering rules, the 24h window |
| `persist/` | saving to and restoring from MongoDB |
| `hub/` | our WebSocket server and the fan-out to clients |
| `auth/` | register, login, KYC, roles |

## The feed (`feed/client.go`)

`runSession` handles one connection: dial, send `auth`, check the reply, send `subscribeMarketPrice` for all 61 symbols, then read ticks in a loop. `Start` wraps it in a retry loop.

- **Reconnect:** if the session ends, wait and try again with exponential backoff (1s, 2s, 4s … max 30s). If the previous session lasted over a minute, retry after 1s again.
- **Dead connections:** a 30s read deadline. If nothing arrives for 30s, the connection is treated as dead and reconnected.
- **Clean close:** every way out of a session (error, shutdown, panic) goes through one `defer` that sends a WebSocket close frame and waits up to 2s for the server's reply before closing the socket. livefxhub allows one session per key and doesn't expire sessions that weren't closed properly, so this matters (see challenges).
- **Shutdown:** Ctrl+C and `docker stop` (SIGINT/SIGTERM) cancel a context that triggers that close.

Each tick is parsed into a `Tick` (symbol, bid, ask, receive time). The feed's own `high`, `low` and `percentage` are ignored, then the tick goes to `store.Update`.

## Cleaning ticks and the 24h window (`store/`)

`Update` decides what happens to each tick:

| Case | Rule |
|---|---|
| same bid and ask as the latest | duplicate, dropped (about 39% of the feed) |
| older than the latest price | out of order: counted in its minute's high/low, but not sent and doesn't replace the latest |
| anything else | becomes the latest, goes into the window, is broadcast |

Order is decided by the feed's timestamp if it has one, otherwise by arrival time. The live feed doesn't send one.

**The 24h window.** Storing every tick for 24h would mean millions of entries. Instead each symbol has a fixed array of **1440 buckets, one per minute**, each holding that minute's open, high and low. A tick goes into slot `minute % 1440`. When the slot still holds a minute from yesterday, it gets reset, so old data is overwritten automatically (a ring buffer).

- 24h high / low = max / min over buckets from the last 1440 minutes
- 24h % change = current bid vs the open of the oldest bucket

Memory stays fixed (about 46 KB per symbol) no matter how busy the market is, and computing stats is a scan of 1440 small structs, about a microsecond.

## Storing in MongoDB (`store/snapshot.go`, `persist/mongo.go`)

Prices are served **from memory**. MongoDB is only there so the 24h data survives a restart.

- **Save:** every 5 seconds, `Snapshot` copies the store under a read lock (microseconds), then the copy is written with **one bulk write**: one document per symbol, `_id` = symbol, upserted. The lock is never held while waiting on the database, so ticks never wait for Mongo. There's one more save on shutdown.
- **Document:** `{_id: "XAUUSD", latest: {...}, buckets: [{minute, open, high, low}, ...]}`. Only non-empty buckets are stored.
- **Restore:** on startup, before the feed connects, each bucket is put back into slot `minute % 1440`. Doing it before the feed matters: otherwise a fresh tick could be overwritten by the older saved one. If loading fails, the app starts empty instead of crashing.
- **Trade-off:** a hard crash (power loss, kill -9) loses at most 5 seconds of ticks. Saving every tick would keep everything, but would put the database in the tick path.

## Our WebSocket server (`hub/`)

- **Connect** (`server.go`, `Handler`): `RequireAuth` (valid JWT) and `RequireKYC` (status verified) run **before** the upgrade, so unverified users get a 403 and never become clients. Each client is then added to the hub.
- **Two goroutines per client:**
  - `readLoop` handles `subscribe` / `unsubscribe`. On subscribe it immediately sends the last known price of each symbol.
  - `writeLoop` is the only thing that writes to the connection. It sends queued messages and pings every 25s. No pong within 60s means the client is dropped.
- **Fan-out** (`hub.go`, `Broadcast`): for each new price, the message is built once and dropped into the 256-slot queue of every client subscribed to that symbol. The hub never waits on a client.
- **Slow clients** (`client.go`, `trySend`): if a client's queue is full, it can't keep up and is disconnected. One slow client never delays the others or the feed.

## Security: KYC and authentication

> *When implementing a new merchant account creation feature, what checks would you add to ensure only verified users can access trading functionality?*

These are the checks I'd add, and the ones this project implements.

**KYC validation**
- Every user has a KYC status: `pending → submitted → verified`, or `rejected` with a reason (they can resubmit).
- Status changes are conditional database updates. A user can't skip a step (`pending` straight to `verified`), and two requests at once can't both succeed.
- Trading functionality (live prices on `/ws`) is behind `RequireKYC`, which checks `verified` **on the server**. The client never decides this.
- The status is read fresh from the database on every request, not from the token. Rejecting someone takes effect on their next request.

**Role-based access**
- Roles: `user`, `merchant`, `admin`. Only an admin can review KYC or change roles.
- **A merchant account can only be created for a KYC-verified user**: the role change is refused otherwise.
- An admin can't approve their own KYC, so it always takes a second person.
- Every KYC decision and role change goes into an audit log with who did it.

**Secure storage of sensitive data**
- Passwords are hashed with bcrypt.
- KYC document numbers are encrypted with AES-256-GCM before they're saved. Only the last 4 characters are kept readable.
- Secrets (feed keys, JWT secret, encryption key) live in `.env`, which is never committed. The app refuses to start with a weak JWT secret or a wrong-sized key.
- Login returns the same error, in about the same time, for a wrong email and a wrong password, so it can't be used to find out who has an account.

In production the manual admin review would be replaced by a KYC provider that checks the ID document and screens sanctions lists. The checks above stay the same.

## Performance

Measured with Docker on my laptop, one client subscribed to every symbol that publishes, over 30 seconds:

| | |
|---|---|
| Tick received → delivered to client | p95 1.8 ms, p99 2.3 ms, max 4.8 ms |
| Message size | ~104 bytes |
| Throughput to one client | ~79 messages/s |
| Snapshot save | 8–19 ms |
| Duplicates dropped | ~39% of feed messages |

## Challenges

- **A session that never expired.** Stopping an early version with Ctrl+C left a session open on livefxhub's side, and every later login got `already have an active session` (4003) for hours. Fixed with graceful shutdown on SIGINT/SIGTERM, a close frame on every exit path, and reconnect with backoff.
- **The close frame sometimes got lost.** The socket was closed straight after sending it while ticks were still arriving, so the connection could be reset before the server read it. Fixed with a proper close handshake (wait for the server's reply, max 2s). Every stop now logs `server confirmed close, session released`.
- **The feed differs from the brief.** The fields are `bid`/`ask`, there's no `timestamp` (so ordering is by arrival), and 22 of the 61 symbols never sent any data during testing.

## Tests

```bash
go test ./...
```

Covers the 24h window, duplicates, out-of-order ticks, hub subscriptions and message size, and the encryption.
