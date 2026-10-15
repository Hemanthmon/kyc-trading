package persist

import (
	"context"
	"log"
	"time"

	"kyc-backend/store"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Saver saves and loads the store's state in MongoDB.
type Saver struct {
	coll *mongo.Collection
}

// New creates a Saver that uses the "pricing" database, "symbol_state" collection.
func New(client *mongo.Client) *Saver {
	return &Saver{
		coll: client.Database("pricing").Collection("symbol_state"),
	}
}

// Save writes all symbols in ONE round trip (bulk write).
// Each symbol's document is replaced, or created if it does not exist yet (upsert).
func (s *Saver) Save(ctx context.Context, states []store.SymbolState) error {
	if len(states) == 0 {
		return nil // nothing to save yet
	}

	writes := make([]mongo.WriteModel, 0, len(states))
	for _, state := range states {
		writes = append(writes, mongo.NewReplaceOneModel().
			SetFilter(bson.M{"_id": state.Symbol}).
			SetReplacement(state).
			SetUpsert(true))
	}

	_, err := s.coll.BulkWrite(ctx, writes)
	return err
}

// Load reads every saved symbol back from MongoDB.
func (s *Saver) Load(ctx context.Context) ([]store.SymbolState, error) {
	cursor, err := s.coll.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}

	var states []store.SymbolState
	if err := cursor.All(ctx, &states); err != nil {
		return nil, err
	}
	return states, nil
}

// Run saves a snapshot every `every` until ctx is cancelled,
// then saves one last time so nothing is lost on shutdown.
func (s *Saver) Run(ctx context.Context, st *store.Store, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.saveNow(st)
		case <-ctx.Done():
			s.saveNow(st) // final save on Ctrl+C
			return
		}
	}
}

func (s *Saver) saveNow(st *store.Store) {
	// Own timeout: ctx may already be cancelled (Ctrl+C), but we still want this save.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	if err := s.Save(ctx, st.Snapshot()); err != nil {
		log.Println("save failed:", err)
		return
	}
	log.Printf("state saved in %s", time.Since(start).Round(time.Millisecond))
}
