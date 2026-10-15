package main

import (
	"context"
	"encoding/json"
	"fmt"
	"kyc-backend/auth"
	"kyc-backend/database"
	"kyc-backend/feed"
	"kyc-backend/hub"
	"kyc-backend/persist"
	"kyc-backend/store"
	"kyc-backend/web"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found")
	}

	// 1. Connect to MongoDB
	client := database.ConnectDB()
	defer client.Disconnect(context.Background())

	// 1b. Accounts, login and KYC (fails fast if secrets are missing, before the feed connects)
	authSvc, err := auth.NewService(client.Database("kyc"), os.Getenv("JWT_SECRET"), os.Getenv("KYC_ENC_KEY"))
	if err != nil {
		log.Fatal("auth setup: ", err)
	}
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = authSvc.Setup(setupCtx, os.Getenv("ADMIN_EMAIL"), os.Getenv("ADMIN_PASSWORD"))
	setupCancel()
	if err != nil {
		log.Fatal("auth setup: ", err)
	}

	// 2. Create the in-memory store and the hub that fans prices out to clients
	st := store.New()
	h := hub.New()

	// 3. Restore saved state BEFORE the feed starts
	saver := persist.New(client)
	loadCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	states, err := saver.Load(loadCtx)
	cancel()
	if err != nil {
		log.Println("could not load saved state, starting empty:", err)
	} else {
		st.Restore(states)
		log.Printf("restored %d symbols from MongoDB", len(states))
	}

	// 4. Ctrl+C (SIGINT), docker stop / closing the terminal (SIGTERM) → ctx cancelled
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 5. Grab port 8080 BEFORE connecting to the feed: if it's busy we exit
	//    here, while no feed session exists yet, so nothing gets stuck.
	ln, err := net.Listen("tcp", ":8080")
	if err != nil {
		log.Fatal("cannot listen on :8080: ", err)
	}

	http.HandleFunc("GET /{$}", web.Handler) // demo page
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/hello", greedHandler)
	http.HandleFunc("GET /stats", statsHandler(st, h))
	authSvc.Routes(http.DefaultServeMux)
	// Live prices = trading functionality: only logged-in, KYC-verified users.
	http.Handle("/ws", authSvc.RequireAuth(auth.RequireKYC(hub.Handler(h, st))))
	go func() {
		if err := http.Serve(ln, nil); err != nil {
			log.Println("http server stopped:", err)
			stop() // shut everything down cleanly instead of log.Fatal
		}
	}()

	// 6. Start the feed
	feedDone := make(chan struct{})
	go func() {
		feed.Start(ctx, st, h)
		close(feedDone)
	}()

	// 7. Save state every 5 seconds
	saverDone := make(chan struct{})
	go func() {
		saver.Run(ctx, st, 5*time.Second)
		close(saverDone)
	}()

	fmt.Println("Hi kyc backend is started")

	// 8. Wait for shutdown signal, then for everything to finish cleanly
	<-ctx.Done()
	h.CloseAll() // tell every client we're going away
	<-feedDone
	<-saverDone
	log.Println("shutdown complete")
}

// statsHandler reports live counters: GET /stats
func statsHandler(st *store.Store, h *hub.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accepted, dups, ooo := st.Counters()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ticks_accepted":     accepted,
			"duplicates_dropped": dups,
			"out_of_order":       ooo,
			"symbols":            st.Len(),
			"clients_online":     h.Count(),
		})
	}
}

func greedHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "Hi bro how are you?")
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprintln(w, "Method not allowed")
		return
	}

	response := struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}{
		Status:  "success",
		Message: "Hemanth KYC backend is healthy",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
