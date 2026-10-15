package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"kyc-backend/hub"
	"kyc-backend/models"
	"kyc-backend/store"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultFeedURL = "wss://v3-prod.livefxhub.com/ws/algo"
	readTimeout    = 30 * time.Second // no message for this long → connection is dead
	maxBackoff     = 30 * time.Second
	closeWait      = 2 * time.Second // on shutdown, how long to wait for the server's close reply
)

var symbols = []string{
	"AUDUSD", "AUDCAD", "AUDCHF", "AUDJPY", "AUDNZD",
	"AUDSGD", "CADCHF", "CADJPY", "CHFJPY", "CHFSGD",
	"EURAUD", "EURCAD", "EURCHF", "EURGBP", "EURJPY",
	"EURNZD", "EURUSD", "GBPAUD", "GBPCAD", "GBPCHF",
	"GBPJPY", "GBPNZD", "GBPUSD", "NZDCAD", "NZDCHF",
	"NZDJPY", "NZDUSD", "USDCAD", "USDCHF", "USDCNH",
	"USDJPY", "USDMXN", "USDNOK", "USDPLN", "USDSEK",
	"USDSGD", "USDTRY", "USDZAR", "EURTRY", "GBPTRY",
	"NOKJPY", "SEKJPY", "SGDJPY", "ZARJPY", "XAUUSD",
	"XAGUSD", "BTCUSD", "ETHUSD", "BNBUSD", "SOLUSD",
	"XRPUSD", "ADAUSD", "DOGUSD", "DOTUSD", "LTCUSD",
	"BCHUSD", "XLMUSD", "TRXUSD", "UNIUSD", "FILUSD",
	"AVXUSD",
}

// Start keeps the feed connected: if a session ends, it waits and reconnects.
// It stops only when ctx is cancelled (Ctrl+C / docker stop).
func Start(ctx context.Context, st *store.Store, h *hub.Hub) {
	backoff := time.Second

	for {
		started := time.Now()
		err := runSession(ctx, st, h)

		if ctx.Err() != nil {
			log.Println("feed stopped")
			return // shutting down → don't reconnect
		}

		// session ran a long time → it was working, so retry quickly
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}

		log.Printf("feed disconnected: %v (retrying in %s)", err, backoff)

		select {
		case <-ctx.Done():
			log.Println("feed stopped")
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// runSession runs ONE connection: dial, auth, subscribe, read ticks.
// Whatever way it ends (error, shutdown, panic), the connection is closed
// with a proper close message so livefxhub releases the session.
func runSession(ctx context.Context, st *store.Store, h *hub.Hub) (err error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, feedURL(), nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	// sayBye sends the WebSocket close frame ("bye"). sync.Once makes sure
	// it is sent only once even if the shutdown goroutine and defer both call it.
	var once sync.Once
	sayBye := func(reason string) {
		once.Do(func() {
			log.Println("closing feed connection:", reason)
			err := conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason),
				time.Now().Add(time.Second),
			)
			if err != nil {
				log.Println("could not send close frame:", err)
			}
		})
	}

	// Every exit path goes through here, even a panic inside the loop.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r) // turn the crash into a normal error → reconnect
		}
		sayBye("session ended")
		conn.Close()
	}()

	// Shutdown (Ctrl+C / docker stop) → proper close handshake:
	// say bye, then keep reading until the server says bye back, THEN close.
	// Closing the line right after our bye (while ticks are still arriving)
	// can make Windows reset the connection before the server reads our bye,
	// and the server then keeps the session "active".
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			sayBye("shutdown")
			select {
			case <-done: // server answered, the read loop ended
			case <-time.After(closeWait):
				log.Println("server did not answer close in time, closing anyway")
				conn.Close()
			}
		case <-done: // session ended on its own → just exit, no goroutine leak
		}
	}()

	// Step 1: auth
	auth := map[string]string{
		"action":  "auth",
		"api_key": os.Getenv("FEED_API_KEY"),
		"secret":  os.Getenv("FEED_SECRET_KEY"),
	}
	if err := conn.WriteJSON(auth); err != nil {
		return fmt.Errorf("send auth: %w", err)
	}

	conn.SetReadDeadline(time.Now().Add(readTimeout))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read auth: %w", err)
	}
	log.Println("auth response:", string(msg))
	if strings.Contains(string(msg), `"event":"error"`) {
		return fmt.Errorf("auth rejected: %s", msg)
	}

	// Step 2: subscribe
	sub := map[string]any{
		"action":  "subscribeMarketPrice",
		"symbols": symbols,
	}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	// Step 3: read ticks until the connection dies or we shut down
	for {
		conn.SetReadDeadline(time.Now().Add(readTimeout)) // silent for 30s → dead
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil && websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Println("server confirmed close, session released")
			}
			return fmt.Errorf("read: %w", err)
		}

		var tick models.Tick
		if err := json.Unmarshal(msg, &tick); err != nil {
			log.Println("bad message:", err)
			continue
		}
		if tick.Event != "marketPriceUpdate" {
			continue
		}
		tick.ReceivedAt = time.Now()

		// New price (not a duplicate) → send it to every subscribed client
		if st.Update(tick) {
			stats, _ := st.Stats(tick.Symbol)
			h.Broadcast(tick.Symbol, hub.PriceMessage(tick, stats))
		}
	}
}

// feedURL returns FEED_URL from .env if set, else the real livefxhub feed.
func feedURL() string {
	if u := os.Getenv("FEED_URL"); u != "" {
		return u
	}
	return defaultFeedURL
}
