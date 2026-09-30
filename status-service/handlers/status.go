package handlers

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/goravaa/chotu/status"
	"github.com/goravaa/chotu/status-service/internals"
)

type StatusHandler struct {
	store *internals.StatusStore
	hub   *internals.StatusHub
}

func NewStatusHandler(
	store *internals.StatusStore, hub *internals.StatusHub) *StatusHandler {
	return &StatusHandler{
		store: store,
		hub:   hub,
	}
}

func (h *StatusHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	current := h.store.Get()

	w.Header().Set("Content-Type", "application/json")

	// The only realistic failure is the client disconnecting mid-body, in which
	// case the response is already truncated and there is nothing to salvage.
	// Logging it is still worth it: a client polling hard enough to trip this
	// is a client worth knowing about.
	if err := json.NewEncoder(w).Encode(current); err != nil {
		log.Printf("[status] could not write the status document: %v", err)
	}
}

// UpdateStatus receives the document the setup daemon publishes after each
// cycle. It is the only way status enters this service, so the page only ever
// shows something the daemon actually observed.
func (h *StatusHandler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	var incoming status.Status

	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	h.store.Update(incoming)
	h.hub.Broadcast(incoming)

	w.WriteHeader(http.StatusNoContent)
}
