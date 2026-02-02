package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"github.com/google/uuid"
)

type CreateRequestInput struct {
	Title string `json:"title"`
}

type CreateRequestOutput struct {
	RequestID string `json:"requestId"`
	Title     string `json:"title"`
}

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("/requests", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var in CreateRequestInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if in.Title == "" {
			http.Error(w, "title required", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		out := CreateRequestOutput{
			RequestID: uuid.NewString(),
			Title:     in.Title,
		}
		if err := json.NewEncoder(w).Encode(out); err != nil {
			http.Error(w, "failed to write response", http.StatusInternalServerError)
			return
		}
	})

	addr := ":8080"
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}