package store

import "kyc-backend/models"

// SymbolState is everything we save for one symbol (one MongoDB document).
type SymbolState struct {
	Symbol  string      `bson:"_id"`
	Latest  models.Tick `bson:"latest"`
	Buckets []Bucket    `bson:"buckets"`
}

// Snapshot copies the current state of every symbol.
// It holds the lock only while copying, never while saving to the DB.
func (s *Store) Snapshot() []SymbolState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	states := make([]SymbolState, 0, len(s.latest))
	for symbol, tick := range s.latest {
		states = append(states, SymbolState{
			Symbol:  symbol,
			Latest:  tick,
			Buckets: s.windows[symbol].nonEmpty(),
		})
	}
	return states
}

// Restore puts saved state back into the store (used at startup).
func (s *Store) Restore(states []SymbolState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, state := range states {
		s.latest[state.Symbol] = state.Latest

		w := &Window{}
		for _, b := range state.Buckets {
			w.buckets[b.Minute%windowMinutes] = b // put each bucket back in its slot
		}
		s.windows[state.Symbol] = w
	}
}
