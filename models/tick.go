package models

import "time"

type Tick struct {
	Event  string  `json:"event"`
	Symbol string  `json:"symbol"`
	Bid    float64 `json:"bid"`
	Ask    float64 `json:"ask"`
	// high, low, percentage deliberately NOT included → we ignore them

	ReceivedAt time.Time `json:"-"` 
}
