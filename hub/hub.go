package hub

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Hub keeps track of every connected client and sends each price
// only to the clients subscribed to that symbol.
type Hub struct {
	mu      sync.RWMutex
	clients map[*Client]struct{} // a "set" of clients
}

func New() *Hub {
	return &Hub{clients: make(map[*Client]struct{})}
}

// add registers a newly connected client.
func (h *Hub) add(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
}

// remove unregisters a client and closes its send queue,
// which tells its writeLoop to stop.
func (h *Hub) remove(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
}

// Broadcast queues msg for every client subscribed to symbol.
// It never waits: a client whose queue is full gets disconnected,
// so one slow client can never delay the others.
func (h *Hub) Broadcast(symbol string, msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if c.wants(symbol) {
			c.trySend(msg)
		}
	}
}

// CloseAll says goodbye to every client (used on shutdown).
func (h *Hub) CloseAll() {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		c.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"),
			time.Now().Add(time.Second),
		)
		c.kick()
	}
}

// Count returns the number of connected clients.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
