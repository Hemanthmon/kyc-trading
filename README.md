# Real-Time Pricing Engine

A Go service that keeps one connection to the livefxhub price feed, cleans the ticks, works out its own rolling 24h high / low / % change for each symbol, and streams the result to its own WebSocket clients. Only logged-in users whose KYC has been approved can receive prices.

Built with Go 1.27, gorilla/websocket and MongoDB. Everything runs with `docker compose up`.

## Contents

- [Running it](#running-it)
- [Trying it out](#trying-it-out)
- [Architecture](#architecture)
- [How it works](#how-it-works)
- [KYC and access control](#kyc-and-access-control)
- [Performance](#performance)
- [Design decisions and trade-offs](#design-decisions-and-trade-offs)
- [Challenges I ran into](#challenges-i-ran-into)
- [What I'd do next](#what-id-do-next)

## Running it

You need Docker Desktop (or Docker Engine with the compose plugin).

```bash
cp .env.example .env
# fill in FEED_API_KEY / FEED_SECRET_KEY, and generate the two secrets:
#   JWT_SECRET=$(openssl rand -hex 32)
#   KYC_ENC_KEY=$(openssl rand -hex 32)
# and set ADMIN_PASSWORD to anything you like

docker compose up --build
```

Then open http://localhost:8080.

This starts two containers: MongoDB (data kept in a named volume) and the app. The app waits until Mongo answers its health check, restarts by itself if it crashes, and on `docker compose stop` it closes the feed connection properly and saves its state before exiting.

An admin account is created on first start from `ADMIN_EMAIL` / `ADMIN_PASSWORD`. You need it to approve KYC.

### Running without Docker

```bash
docker compose up -d mongo     # just the database
go run .                        # uses MONGODB_URI=mongodb://localhost:27017 from .env
go test ./...
```

Don't run `go run .` and the Docker app at the same time. They both want port 8080, and livefxhub only allows one session per API key.

## Trying it out

The easiest way is the page at http://localhost:8080. It walks through the whole flow:

1. Register and log in. New accounts start with KYC `pending`.
2. Click **Connect** under live prices. It gets refused, because KYC isn't verified yet.
3. Submit KYC (name, document type, document number).
4. Log in as the admin and approve it.
5. Connect again. Prices start streaming, with message size and latency shown for every row.

The stats box shows ticks accepted, duplicates dropped, out-of-order ticks and clients online.

### HTTP API

| Method | Path | Who | What it does |
|---|---|---|---|
| POST | `/auth/register` | anyone | create an account `{email, password}` |
| POST | `/auth/login` | anyone | returns `{token, expires_in, user}` |
| GET | `/me` | logged in | the current user |
| POST | `/kyc/submit` | logged in | `{full_name, document_type, document_number}` |
| GET | `/admin/kyc/pending` | admin | users waiting for review |
| POST | `/admin/kyc/review` | admin | `{user_id, decision: "approve"\|"reject", reason}` |
| POST | `/admin/users/role` | admin | `{user_id, role: "user"\|"merchant"\|"admin"}` |
| GET | `/stats` | anyone | live counters |
| GET | `/health` | anyone | health check |

Send the token as `Authorization: Bearer <token>`.

### WebSocket: `/ws`

Browsers can't set headers on a WebSocket, so the token goes in the URL: `ws://localhost:8080/ws?token=<token>`. The user must be KYC-verified, otherwise the upgrade is refused with 401 or 403.

Client to server:

```json
{"action": "subscribe",   "symbols": ["XAUUSD", "EURUSD"]}
{"action": "unsubscribe", "symbols": ["EURUSD"]}
```

Server to client, one message per price change:

```json
{"s":"XAUUSD","b":4155.875,"a":4156.155,"h":4162.245,"l":4111.42,"c":1.04,"t":1792070251654}
```

`s` symbol, `b` bid, `a` ask, `h`/`l` 24h high/low, `c` 24h % change, `t` the time the server received the tick (unix ms). `h`, `l` and `c` are computed here. The feed's own `high`, `low` and `percentage` fields are ignored, as the assignment asks.

Right after a subscribe, the client gets the last known price of each symbol, so it doesn't sit on an empty screen waiting for the next tick.

## Architecture

```
                          pricing engine (one Go process, :8080)
               ┌───────────────────────────────────────────────────────────────┐
               │                                                               │
 livefxhub ───►│  feed/  ──────►  store/  ──────────►  hub/  ──────────────────│──► WebSocket clients
 (wss, one     │  connect, auth,  latest tick per      one queue + writer per   │    (only KYC-verified)
  session)     │  subscribe,      symbol, drop dups    client, send only the    │
               │  reconnect       and late ticks,      subscribed symbols       │
               │                  1440 one-minute         ▲                     │
               │                  buckets per symbol      │ RequireAuth         │
               │                      │                   │ RequireKYC          │
               │                      │ every 5s       auth/  ◄─────────────────│─── HTTP: register, login,
               │                      ▼                (JWT, bcrypt, roles,    │    KYC, admin review
               │                  persist/             AES-GCM for ID numbers) │
               │                  snapshot / restore      │                    │
               └──────────────────────┼──────────────────┼────────────────────┘
                                      ▼                  ▼
                              MongoDB: pricing     MongoDB: kyc
                              (symbol_state)       (users, kyc_audit)
```

| Package | Responsibility |
|---|---|
| `feed` | the upstream client: auth, subscribe, read loop, reconnect with backoff, clean close |
| `store` | latest tick per symbol, duplicate and ordering rules, the 24h window, snapshot and restore |
| `persist` | saving snapshots to MongoDB every 5s and loading them at startup |
| `hub` | the WebSocket server: clients, subscriptions, fan-out, ping/pong |
| `auth` | accounts, JWT, KYC state machine, role checks, audit log |
| `secure` | AES-256-GCM for KYC document numbers |
| `web` | the demo page, embedded in the binary |
| `main.go` | wiring, startup order, graceful shutdown |

`store` doesn't know MongoDB exists and `hub` doesn't know where prices come from. Each package can be tested without the others, and swapping MongoDB or the feed only touches one package.

## How it works

### The feed

One goroutine owns the livefxhub connection. It authenticates, subscribes to all 61 symbols, then reads ticks in a loop. If the connection drops, it reconnects with exponential backoff (1s, 2s, 4s… capped at 30s), resetting to 1s if the previous session lasted more than a minute. A 30s read deadline catches a connection that dies silently without an error.

Every exit path (an error, a shutdown signal, even a panic in the loop) goes through one `defer` that sends a WebSocket close frame and waits up to 2s for the server's reply before dropping the TCP connection. That matters here, see [challenges](#challenges-i-ran-into).

### Cleaning ticks

`store.Update` decides what happens to each tick:

- **Duplicate**: same bid and ask as the latest price for that symbol. Dropped. This is about 39% of everything the feed sends.
- **Out of order**: older than the latest price we already have. It still goes into its own minute's high/low, because it's a real price that happened, but it never replaces the latest price and is never sent to clients.
- **Otherwise** it becomes the latest price, goes into the 24h window, and is broadcast.

Ordering uses the feed's `timestamp` if there is one, otherwise the time we received it. The live feed doesn't actually send a timestamp, even though the brief says it does, so in practice ordering is by arrival.

### The rolling 24h window

Keeping every tick for 24 hours isn't practical. At a few hundred ticks a second that's tens of millions of entries, and scanning them on every tick is far too slow.

Instead each symbol has a fixed array of 1440 buckets, one per minute. A bucket stores the minute it belongs to plus that minute's open, high and low. A tick goes into slot `minute % 1440`. If the slot holds a different (older) minute, it's reset first, so the array wraps around and data older than 24h is overwritten without any cleanup job.

- 24h high / low = max / min over the buckets from the last 1440 minutes
- 24h % change = current bid vs the open of the oldest bucket in the window

Memory is fixed at about 46 KB per symbol however busy the market is, and computing the stats is a scan of 1440 small structs (around a microsecond). Stats use the bid price.

### Surviving restarts

Every 5 seconds the saver takes a snapshot of the store (latest tick plus non-empty buckets for each symbol). It holds the store's read lock only while copying, never while talking to the database. The snapshot is written to MongoDB in one bulk write, one document per symbol with the symbol as `_id`, upserted. On shutdown it saves once more.

On startup, state is loaded and restored before the feed connects. Otherwise a fresh tick could arrive and then be overwritten by the older saved one. If the load fails, the app logs it and starts empty rather than refusing to run.

### Fan-out to clients

Each client gets a buffered send queue (256 messages) and its own writer goroutine, which is the only thing allowed to write to that connection. When a price changes, the hub drops the same pre-built message into the queue of every client subscribed to that symbol and moves on. It never waits on a client.

If a client's queue is full, it can't keep up, so it's disconnected. A slow phone on a bad network only ever hurts itself, never the other clients or the feed.

The writer pings every 25s, and the reader drops the client if no pong arrives within 60s. Incoming messages are capped at 4 KB.

### Shutdown

SIGINT (Ctrl+C) and SIGTERM (`docker stop`) cancel one context that everything watches. Order: tell clients the server is going away (close code 1001), close the feed with a proper handshake, do a final save, exit. Under Docker that takes about a second, well inside the 20s `stop_grace_period`.

## KYC and access control

The question from the brief: *what checks would you add so only verified users can access trading functionality?*

**KYC state machine.** Every user has `kyc.status`:

```
pending ──submit──► submitted ──approve──► verified
                        │
                        └──reject (reason)──► rejected ──resubmit──► submitted
```

Transitions are done as conditional updates (`UpdateOne` filtered on the current status), so two requests at once can't both succeed, and you can't jump from `pending` to `verified`.

**Live prices are gated server-side.** `/ws` is wrapped in two middlewares: `RequireAuth` (valid JWT, user exists) and then `RequireKYC` (status is `verified`). Both run before the WebSocket upgrade, so an unverified user gets a plain 403 and never becomes a hub client.

**Role-based access.** Roles are `user`, `merchant` and `admin`. Only admins can list and review KYC or change roles. Becoming a merchant or admin requires verified KYC first, and an admin can't approve their own KYC.

**Fresh checks.** The JWT only says who you are (`sub` = user id). Role and KYC status are read from the database on every request, so approving or revoking someone takes effect on their next request instead of when their token expires. For the WebSocket that's one lookup per connection, not per tick.

**Sensitive data.**

- Passwords are hashed with bcrypt.
- KYC document numbers are encrypted with AES-256-GCM before they're stored. Only the last 4 characters are kept in plain text, for display.
- Secrets (feed keys, JWT secret, encryption key) come from the environment and never go in the repo. The app refuses to start if the JWT secret is shorter than 32 characters or the encryption key isn't 32 bytes.
- Login gives the same error and takes about the same time whether the email exists or not, so it can't be used to find out who has an account.
- Every KYC submission, decision and role change is written to an audit collection with who did it.

In a real product, the manual admin review would be replaced by a KYC provider (Onfido, Sumsub and the like) that checks the document, matches a selfie and screens sanctions lists, then calls back with the result. Everything around that step stays the same.

## Performance

Measured on my laptop with the Docker setup, one client subscribed to the 39 symbols that actually publish, over 30 seconds:

| Metric | Result |
|---|---|
| Messages delivered | 2,372 (about 79/s) |
| Tick received by server → client, p95 | 1.8 ms |
| p99 | 2.3 ms |
| max | 4.8 ms |
| Message size | 104 bytes average, 108 max |
| Duplicate ticks dropped | about 39% of the feed |
| Snapshot save to local MongoDB | 8–19 ms (it was 100–150 ms on Atlas) |
| App image size | about 32 MB |

The latency is measured from the moment the tick is read off the feed connection to the moment the client reads it. Client and server are on the same machine, but the container's clock can be a fraction of a millisecond off the host's, so the median comes out at about zero.

On payload: one feed message is about 135 bytes, and with ~40% duplicates and every symbol arriving whether you want it or not, the raw input adds up quickly. A client only gets the symbols it subscribed to, never a duplicate, and each message is around 100 bytes thanks to short keys and no unused fields. The largest thing a client ever receives is the snapshot right after subscribing to everything: 39 small messages, about 10 KB in total.

The 5s save never touches the tick path. It runs in its own goroutine on a copy of the data.

## Design decisions and trade-offs

- **Memory first, MongoDB as a backup.** Serving prices from MongoDB would blow the latency budget on its own (one Atlas round trip was over 100 ms). The trade-off is that a hard crash (kill -9, power loss) loses up to 5s of ticks. For 24h high/low that's acceptable. Saving on every tick would remove that window but put the database on the hot path.
- **One-minute buckets.** High and low are exact, since every tick updates its bucket. The "24h ago" price for % change is the open of the oldest minute, so it can be up to a minute off. I think that's a fair price for fixed memory and constant-time stats.
- **Kick slow clients instead of buffering forever.** Simple and keeps everyone else fast. The nicer alternative is conflation: keep only the latest price per symbol for a slow client and skip the ones in between. I'd do that next.
- **JSON with short keys, not binary.** At ~100 bytes a message the payload is already far under the limit, and JSON is easy to debug in a browser. MessagePack or protobuf would roughly halve it if it ever mattered.
- **Single instance.** The hub lives in memory, so this is one process. To scale out, run several instances behind a load balancer and share ticks through Redis pub/sub (or Kafka if you also want replay). I didn't add those because a single instance doesn't need them, and they'd add latency and moving parts.
- **One access token, no refresh tokens.** Tokens last an hour (HS256). Revoking trading access is still instant because KYC is checked on every request. What's missing is a real logout. Production would use short access tokens plus rotating refresh tokens stored in the database.
- **KYC is checked when a WebSocket connects.** Rejecting a user who is already connected doesn't cut them off until they reconnect. The fix is for the review endpoint to also kick that user's connections in the hub.
- **`CheckOrigin` allows any origin.** That's needed for local testing. In production it should only allow your own domain.

## Challenges I ran into

**A session that never expired.** Early on I stopped the app with Ctrl+C before it had any shutdown handling. After that every login got `already have an active session` (close code 4003) for hours. Nothing on my machine was connected (I checked the open TCP connections), so the session only existed on livefxhub's side, and it doesn't seem to time them out. I had to ask them to reset it.

The fix was to make sure the app always says goodbye. It handles SIGINT and SIGTERM, every exit path sends a close frame, port 8080 is claimed before the feed connects (so a failed start can't leave a session behind), and the feed retries with backoff, so it reconnects by itself once a session frees up.

**The goodbye sometimes got lost.** Even with a close frame, a clean stop occasionally still left the session stuck. My best explanation: the code sent the close frame and then closed the socket immediately while ticks were still arriving, and on Windows closing a socket with unread data sends a TCP reset, so the server can drop the close frame. It now does the proper WebSocket close handshake: send the close frame, keep reading until the server answers, then close (giving up after 2s). Since then the logs show `server confirmed close, session released` on every stop, and the next start connects straight away.

**The feed doesn't match the brief.** The fields are `bid`/`ask` rather than `buy`/`sell`, and there's no `timestamp`, so I order ticks by arrival time and use a feed timestamp only if one shows up. 22 of the 61 symbols never sent a single tick while I was testing: AUDSGD, AVXUSD, BCHUSD, CHFSGD, DOGUSD, DOTUSD, ETHUSD, FILUSD, GBPCAD, GBPCHF, GBPJPY, GBPTRY, LTCUSD, NOKJPY, SEKJPY, SGDJPY, SOLUSD, TRXUSD, UNIUSD, USDPLN, XLMUSD and ZARJPY. They're still subscribed and will work if data appears.

**Lots of duplicates.** About 39% of feed messages repeat the previous price exactly. Dropping them before they reach the store and the hub is the single biggest saving in output traffic.

**Atlas connection errors.** MongoDB Atlas started failing with `tls: internal error`, which turned out to be my IP changing and no longer being on the Atlas allowlist. For the submission I moved to a MongoDB container in compose, which also makes `docker compose up` self-contained.

## What I'd do next

- Conflation for slow clients instead of disconnecting them
- Kick a user's open connections when their KYC is rejected
- Refresh tokens and logout
- Rate limiting on login and on subscribe messages
- Prometheus metrics (tick rate, latency histogram, queue depths) in place of the simple `/stats`
- OHLC candles: the minute buckets already have open, high and low, so this mostly needs a close price and an endpoint
- A KYC provider integration instead of manual review

## Tests

```bash
go test ./...
```

Covers the 24h window (stats, data expiring after 24h, late ticks not overwriting newer buckets), duplicate and out-of-order handling, the hub (subscribe, only receiving subscribed symbols, message size) and the encryption (round trip, tamper detection, bad keys).
