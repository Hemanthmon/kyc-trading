package models

import "time"

type Tick struct {
	Event  string  `json:"event"`
	Symbol string  `json:"symbol"`
	Bid    float64 `json:"bid"`
	Ask    float64 `json:"ask"`
	// high, low, percentage deliberately NOT included → we ignore them

	// Timestamp is the feed's own time for this price, if it sends one
	// (the PDF says it does; the live feed currently doesn't). Seconds or ms.
	Timestamp int64 `json:"timestamp,omitempty"`

	ReceivedAt time.Time `json:"-"` // our own timestamp, not from JSON
}

// EventTime is when this price happened: the feed's timestamp if present,
// otherwise the time we received it. All ordering decisions use this.
func (t Tick) EventTime() time.Time {
	switch {
	case t.Timestamp > 1e12: // milliseconds
		return time.UnixMilli(t.Timestamp)
	case t.Timestamp > 0: // seconds
		return time.Unix(t.Timestamp, 0)
	default:
		return t.ReceivedAt
	}
}
