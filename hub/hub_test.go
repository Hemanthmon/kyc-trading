package hub

import (
	"encoding/json"
	"kyc-backend/models"
	"kyc-backend/store"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dial connects a test client to the server and subscribes to symbols.
func dial(t *testing.T, url string, symbols ...string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal("dial:", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.WriteJSON(request{Action: "subscribe", Symbols: symbols}); err != nil {
		t.Fatal("subscribe:", err)
	}
	return conn
}

func read(t *testing.T, conn *websocket.Conn) (priceMsg, error) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var m priceMsg
	err := conn.ReadJSON(&m)
	return m, err
}

func TestSubscribeAndBroadcast(t *testing.T) {
	st := store.New()
	h := New()
	srv := httptest.NewServer(Handler(h, st))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	// A price already exists before anyone connects.
	st.Update(models.Tick{Symbol: "XAUUSD", Bid: 4155, Ask: 4156, ReceivedAt: time.Now()})

	gold := dial(t, url, "XAUUSD")
	euro := dial(t, url, "EURUSD")

	// 1. On subscribe, the gold client gets the current price immediately.
	m, err := read(t, gold)
	if err != nil || m.S != "XAUUSD" || m.B != 4155 {
		t.Fatalf("snapshot on subscribe: got %+v, err %v", m, err)
	}

	// Wait until both clients are registered before broadcasting.
	for h.Count() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let the euro client's subscribe be processed

	// 2. A new gold tick goes to the gold client only.
	tick := models.Tick{Symbol: "XAUUSD", Bid: 4160, Ask: 4161, ReceivedAt: time.Now()}
	st.Update(tick)
	stats, _ := st.Stats("XAUUSD")
	h.Broadcast("XAUUSD", PriceMessage(tick, stats))

	m, err = read(t, gold)
	if err != nil || m.B != 4160 || m.H != 4160 || m.L != 4155 {
		t.Fatalf("gold client: got %+v, err %v", m, err)
	}
	if m, err := read(t, euro); err == nil {
		t.Fatalf("euro client should not receive gold prices, got %+v", m)
	}
}

func TestMessageIsSmall(t *testing.T) {
	msg := PriceMessage(
		models.Tick{Symbol: "XAUUSD", Bid: 4155.875, Ask: 4156.155, ReceivedAt: time.Now()},
		store.Stats{High: 4162.245, Low: 4111.42, ChangePct: 1.04},
	)
	var back priceMsg
	if err := json.Unmarshal(msg, &back); err != nil {
		t.Fatal(err)
	}
	t.Logf("one price message = %d bytes: %s", len(msg), msg)
	if len(msg) > 200 {
		t.Errorf("message too big: %d bytes", len(msg))
	}
}
