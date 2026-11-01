// Command badservice is a made-up service with the mistakes gosturdy looks for.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
)

func handle(w http.ResponseWriter, r *http.Request) {
	var order map[string]any
	if err := json.NewDecoder(r.Body).Decode(&order); err != nil {
		http.Error(w, "bad order", http.StatusBadRequest)
		return
	}
	go func() {
		audit(r.Context(), order)
	}()
	resp, err := http.Get("https://payments.example.com/charge")
	if err != nil {
		http.Error(w, "payment failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	audit(context.Background(), order)
}

func audit(ctx context.Context, order map[string]any) {
	_, _ = ctx, order
}

func main() {
	db, err := sql.Open("postgres", "made-up-connection")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	http.HandleFunc("/orders", handle)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
