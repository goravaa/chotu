package main

import (
	"log"
	"net/http"

	"github.com/goravaa/chotu/status-service/handlers"
	"github.com/goravaa/chotu/status-service/internals"

	"github.com/go-chi/chi/v5"
)

func main() {
	store := &internals.StatusStore{}
	hub := internals.NewStatusHub()

	statusHandler := handlers.NewStatusHandler(store, hub)
	eventHandler := handlers.NewEventHandler(hub)
	r := chi.NewRouter()

	r.Get("/api/v1/status", statusHandler.GetStatus)
	r.Post("/api/v1/status", statusHandler.UpdateStatus)
	r.Get("/api/v1/events", eventHandler.Events)
	r.Get("/", handlers.Page)
	log.Println("status service listening on :8081")

	if err := http.ListenAndServe(":8081", r); err != nil {
		log.Fatal(err)
	}
}
