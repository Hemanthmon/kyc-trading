package store

import (
	"kyc-backend/models"
	"sync"
	"time"
)

// Store keeps the latest tick for every symbol in memory (RAM).
type Store struct {
	mu      sync.RWMutex           // protects latest: many readers OR one writer at a time
	latest  map[string]models.Tick // "EURUSD" → latest EURUSD tick
	windows map[string]*Window

	// counters for monitoring (protected by mu)
	accepted, duplicates, outOfOrder uint64
}

// New creates an empty Store.
// A map must be created with make() before use, or writing to it panics.
func New() *Store {
	return &Store{
		latest:  make(map[string]models.Tick),
		windows: make(map[string]*Window),
	}
}

// Update saves the tick as the latest price for its symbol.
// It returns true only for a new, in-order price, so the caller knows to send it.
//
//   - duplicate (same bid and ask as the latest)      → dropped, false
//   - out of order (older than the latest we have)    → counted in its own minute's
//     24h high/low (it is a real price), but never replaces the latest or gets sent, false
func (s *Store) Update(t models.Tick) bool {
	s.mu.Lock()         // writing → exclusive lock
	defer s.mu.Unlock() // unlock automatically when the function returns

	w, ok := s.windows[t.Symbol]
	if !ok {
		w = &Window{} // first tick of this symbol → create its window
		s.windows[t.Symbol] = w
	}

	old, exists := s.latest[t.Symbol]
	if exists && t.EventTime().Before(old.EventTime()) {
		s.outOfOrder++
		w.Add(t.Bid, t.EventTime())
		return false
	}
	if exists && old.Bid == t.Bid && old.Ask == t.Ask {
		s.duplicates++
		return false // duplicate: price did not change
	}

	s.accepted++
	s.latest[t.Symbol] = t
	w.Add(t.Bid, t.EventTime())
	return true
}

// Counters returns how many ticks were accepted, dropped as duplicates,
// and handled as out of order since startup.
func (s *Store) Counters() (accepted, duplicates, outOfOrder uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accepted, s.duplicates, s.outOfOrder
}

// Len returns how many symbols have received at least one price.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.latest)
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
