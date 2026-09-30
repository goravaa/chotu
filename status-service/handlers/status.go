package handlers

import (
	"encoding/json"
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
	json.NewEncoder(w).Encode(current)
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
