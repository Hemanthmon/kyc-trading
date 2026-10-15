package hub

import (
	"sync"

	"github.com/gorilla/websocket"
)

const sendQueueSize = 256 // messages a client may fall behind before we kick it

// Client is one connected WebSocket client.
type Client struct {
	conn *websocket.Conn
	send chan []byte // outgoing messages, drained by writeLoop

	mu   sync.RWMutex
	subs map[string]bool // symbols this client wants, e.g. {"XAUUSD": true}

	kickOnce sync.Once
}

func newClient(conn *websocket.Conn) *Client {
	return &Client{
		conn: conn,
		send: make(chan []byte, sendQueueSize),
		subs: make(map[string]bool),
	}
}

// wants reports whether the client is subscribed to symbol.
func (c *Client) wants(symbol string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.subs[symbol]
}

// trySend queues msg without ever waiting; a full queue means the client is too slow.
func (c *Client) trySend(msg []byte) {
	select {
	case c.send <- msg:
	default:
		c.kick()
	}
}

// kick disconnects the client. Closing the connection makes readLoop fail,
// which then unregisters the client. sync.Once: safe to call many times.
func (c *Client) kick() {
	c.kickOnce.Do(func() {
		c.conn.Close()
	})
}
