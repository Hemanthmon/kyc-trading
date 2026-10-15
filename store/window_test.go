package store

import (
	"testing"
	"time"
)

func TestWindowStats(t *testing.T) {
	var w Window
	start := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC) // fake "10:00"

	// minute 10:00 → prices 100, 105, 98
	w.Add(100, start)
	w.Add(105, start.Add(10*time.Second))
	w.Add(98, start.Add(20*time.Second))

	// minute 10:01 → price 110
	w.Add(110, start.Add(1*time.Minute))

	now := start.Add(1 * time.Minute)
	st, ok := w.Stats(now, 110)

	if !ok {
		t.Fatal("expected stats, got none")
	}
	if st.High != 110 {
		t.Errorf("High = %v, want 110", st.High)
	}
	if st.Low != 98 {
		t.Errorf("Low = %v, want 98", st.Low)
	}
	if st.ChangePct != 10 { // (110 - 100) / 100 * 100 = 10%
		t.Errorf("ChangePct = %v, want 10", st.ChangePct)
	}
}

func TestWindowDropsOldData(t *testing.T) {
	var w Window
	start := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)

	w.Add(50, start)                    // very low price at 10:00 yesterday
	w.Add(100, start.Add(23*time.Hour)) // normal price 23h later

	now := start.Add(25 * time.Hour) // 25h after the first tick
	st, ok := w.Stats(now, 100)

	if !ok {
		t.Fatal("expected stats, got none")
	}
	if st.Low != 100 {
		t.Errorf("Low = %v, want 100 (the 50 is older than 24h and must be ignored)", st.Low)
	}
}
