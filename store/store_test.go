package store

import (
	"kyc-backend/models"
	"testing"
	"time"
)

func tick(bid float64, at time.Time) models.Tick {
	return models.Tick{Event: "marketPriceUpdate", Symbol: "XAUUSD", Bid: bid, Ask: bid + 0.3, Timestamp: at.UnixMilli()}
}

func TestDuplicateDropped(t *testing.T) {
	st := New()
	now := time.Now()

	if !st.Update(tick(4156, now)) {
		t.Fatal("first tick should be accepted")
	}
	if st.Update(tick(4156, now.Add(time.Second))) {
		t.Error("same price again should be dropped as a duplicate")
	}
	if _, dup, _ := st.Counters(); dup != 1 {
		t.Errorf("duplicates = %d, want 1", dup)
	}
}

func TestOutOfOrderTick(t *testing.T) {
	st := New()
	now := time.Now()

	st.Update(tick(4156, now))                             // newest price
	sent := st.Update(tick(4100, now.Add(-2*time.Second))) // arrives late, from 2s earlier

	if sent {
		t.Error("late tick must not be sent to clients")
	}
	latest, _ := st.Get("XAUUSD")
	if latest.Bid != 4156 {
		t.Errorf("latest bid = %v, want 4156 (late tick must not overwrite it)", latest.Bid)
	}
	stats, _ := st.Stats("XAUUSD")
	if stats.Low != 4100 {
		t.Errorf("24h low = %v, want 4100 (late tick is still a real price)", stats.Low)
	}
	if _, _, ooo := st.Counters(); ooo != 1 {
		t.Errorf("outOfOrder = %d, want 1", ooo)
	}
}

func TestNoTimestampUsesReceivedAt(t *testing.T) {
	st := New()
	now := time.Now()

	a := models.Tick{Symbol: "EURUSD", Bid: 1.1, Ask: 1.2, ReceivedAt: now}
	b := models.Tick{Symbol: "EURUSD", Bid: 1.3, Ask: 1.4, ReceivedAt: now.Add(time.Millisecond)}
	if !st.Update(a) || !st.Update(b) {
		t.Fatal("in-order ticks without a feed timestamp should be accepted")
	}
}

func TestLateTickDoesNotWipeNewerMinute(t *testing.T) {
	var w Window
	start := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)

	w.Add(100, start.Add(24*time.Hour)) // today 10:00
	w.Add(50, start)                    // yesterday 10:00, same slot, arrives late

	st, ok := w.Stats(start.Add(24*time.Hour), 100)
	if !ok || st.Low != 100 {
		t.Errorf("Low = %v, want 100 (a 24h-late tick must not replace today's bucket)", st.Low)
	}
}
