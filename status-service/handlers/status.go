package handlers

import (
	"encoding/json"
	"net/http"

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
	status := h.store.Get()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (h *StatusHandler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	var status internals.Status

	if err := json.NewDecoder(r.Body).Decode(&status); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	h.store.Update(status)
	h.hub.Broadcast(status)

	w.WriteHeader(http.StatusNoContent)
}
