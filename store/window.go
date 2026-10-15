package store

import "time"

const windowMinutes = 24 * 60

type Bucket struct {
	Minute int64
	Open   float64
	High   float64
	Low    float64
}

type Stats struct {
	High      float64
	Low       float64
	ChangePct float64 // % change vs 24h ago
}

// Window holds the last 24 hours for ONE symbol: 1440 one-minute buckets.
type Window struct {
	buckets [windowMinutes]Bucket
}

func minuteOf(t time.Time) int64 {
	return t.Unix() / 60
}

func (w *Window) Add(price float64, at time.Time) {
	m := minuteOf(at)
	b := &w.buckets[m%windowMinutes]

	if b.Minute > m {
		return // slot already holds a newer minute → this price is over 24h late, ignore it
	}
	if b.Minute != m {
		// slot is empty or holds a minute from 24h ago → start a fresh bucket
		*b = Bucket{Minute: m, Open: price, High: price, Low: price}
		return
	}
	if price > b.High {
		b.High = price
	}
	if price < b.Low {
		b.Low = price
	}

}

func (w *Window) Stats(now time.Time, current float64) (st Stats, ok bool) {

	nowMin := minuteOf(now)
	oldest := nowMin - windowMinutes + 1

	var openMin int64
	var openPrice float64

	for i := range w.buckets {
		b := w.buckets[i]

		if b.Minute < oldest || b.Minute > nowMin {
			continue // empty, or older than 24h → ignore
		}

		if !ok {
			// first valid bucket: start with its values
			st.High, st.Low = b.High, b.Low
			openMin, openPrice = b.Minute, b.Open
			ok = true
			continue
		}

		if b.High > st.High {
			st.High = b.High
		}

		if b.Low < st.Low {
			st.Low = b.Low
		}
		if b.Minute < openMin { // found an even older bucket
			openMin, openPrice = b.Minute, b.Open
		}
	}
	if !ok {
		return st, false // no data at all in the last 24h
	}

	st.ChangePct = (current - openPrice) / openPrice * 100
	return st, true
}

// nonEmpty returns only the buckets that have data (for saving).
func (w *Window) nonEmpty() []Bucket {
	var out []Bucket
	for i := range w.buckets {
		if w.buckets[i].Minute != 0 {
			out = append(out, w.buckets[i])
		}
	}
	return out
}
