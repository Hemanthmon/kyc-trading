package store

import (
	"kyc-backend/models"
	"sync"
	"time"
)

// Store keeps the latest tick for every symbol in memory (RAM).
type Store struct {
	mu     sync.RWMutex           // protects latest: many readers OR one writer at a time
	latest map[string]models.Tick // "EURUSD" → latest EURUSD tick
	windows map[string]*Window
}

// New creates an empty Store.
// A map must be created with make() before use, or writing to it panics.
func New() *Store {
	return &Store{
		latest: make(map[string]models.Tick),
		windows: make(map[string]*Window),

	}
}

// Update saves the tick as the latest price for its symbol.
// It returns false if the tick is a duplicate (same bid and ask as last time),
// so the caller knows there is nothing new to send.
func (s *Store) Update(t models.Tick) bool {
	s.mu.Lock()         // writing → exclusive lock
	defer s.mu.Unlock() // unlock automatically when the function returns

	old, exists := s.latest[t.Symbol]
	if exists && old.Bid == t.Bid && old.Ask == t.Ask {
		return false // duplicate: price did not change
	}

	s.latest[t.Symbol] = t

	w, ok := s.windows[t.Symbol]
	if !ok {
		w = &Window{}             // first tick of this symbol → create its window
		s.windows[t.Symbol] = w
	}
	w.Add(t.Bid, t.ReceivedAt)


	return true
}

// Get returns the latest tick for a symbol.
// ok is false if we have never received that symbol.
func (s *Store) Get(symbol string) (tick models.Tick, ok bool) {
	s.mu.RLock() // reading → shared lock (many readers can read together)
	defer s.mu.RUnlock()

	tick, ok = s.latest[symbol]
	return tick, ok
}


func (s *Store) Stats(symbol string) (Stats, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.latest[symbol]
	if !ok {
		return Stats{}, false // never seen this symbol
	}
	return s.windows[symbol].Stats(time.Now(), t.Bid)
}