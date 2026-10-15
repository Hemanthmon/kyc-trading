package hub

import (
	"encoding/json"
	"kyc-backend/models"
	"kyc-backend/store"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait  = 5 * time.Second  // max time to write one message
	pongWait   = 60 * time.Second // no pong for this long → client is dead
	pingEvery  = 25 * time.Second // must be less than pongWait
	maxMsgSize = 4096             // biggest message we accept from a client
)

var upgrader = websocket.Upgrader{
	// Allow any origin so you can test from the browser console.
	// In production, check r.Header.Get("Origin") against your own domain.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// request is what a client sends: {"action":"subscribe","symbols":["XAUUSD"]}
type request struct {
	Action  string   `json:"action"`
	Symbols []string `json:"symbols"`
}

// priceMsg is what we send. Short keys keep each message small.
type priceMsg struct {
	S string  `json:"s"` // symbol
	B float64 `json:"b"` // bid
	A float64 `json:"a"` // ask
	H float64 `json:"h"` // 24h high (our own)
	L float64 `json:"l"` // 24h low (our own)
	C float64 `json:"c"` // 24h % change (our own)
	T int64   `json:"t"` // time we received the tick, unix ms
}

// PriceMessage builds the JSON message sent to clients for one tick.
func PriceMessage(t models.Tick, s store.Stats) []byte {
	msg, _ := json.Marshal(priceMsg{
		S: t.Symbol, B: t.Bid, A: t.Ask,
		H: s.High, L: s.Low, C: s.ChangePct,
		T: t.ReceivedAt.UnixMilli(),
	})
	return msg
}

// Handler upgrades GET /ws to a WebSocket and serves one client until it leaves.
func Handler(h *Hub, st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("ws upgrade:", err)
			return
		}

		c := newClient(conn)
		h.add(c)
		log.Printf("client connected (%d online)", h.Count())

		go c.writeLoop()
		c.readLoop(st) // blocks until the client leaves

		h.remove(c)
		c.kick()
		log.Printf("client left (%d online)", h.Count())
	}
}

// readLoop handles subscribe/unsubscribe messages from the client.
func (c *Client) readLoop(st *store.Store) {
	c.conn.SetReadLimit(maxMsgSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait)) // client is alive
		return nil
	})

	for {
		var req request
		if err := c.conn.ReadJSON(&req); err != nil {
			return // client left, connection broke, or bad JSON
		}

		switch req.Action {
		case "subscribe":
			c.mu.Lock()
			for _, s := range req.Symbols {
				c.subs[s] = true
			}
			c.mu.Unlock()

			// Send the current price right away, so the client
			// doesn't have to wait for the next tick.
			for _, s := range req.Symbols {
				if t, ok := st.Get(s); ok {
					stats, _ := st.Stats(s)
					c.trySend(PriceMessage(t, stats))
				}
			}

		case "unsubscribe":
			c.mu.Lock()
			for _, s := range req.Symbols {
				delete(c.subs, s)
			}
			c.mu.Unlock()

		default:
			c.trySend([]byte(`{"error":"unknown action"}`))
		}
	}
}

// writeLoop is the ONLY goroutine that writes to the connection.
// It sends queued messages and pings the client to check it is alive.
func (c *Client) writeLoop() {
	ticker := time.NewTicker(pingEvery)
	defer ticker.Stop()
	defer c.kick()

	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				return // hub closed the queue → client removed
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
