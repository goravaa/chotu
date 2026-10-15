package handlers

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/goravaa/chotu/status-service/internals"
)

type MetaHandler struct {
	keeper *internals.Keeper
}

func NewMetaHandler(keeper *internals.Keeper) *MetaHandler {
	return &MetaHandler{keeper: keeper}
}

// GetMeta serves the keeper's snapshot: service uptime plus per-task timings.
// It is the only endpoint that reads the keeper, so the timing display never
// interferes with the status document round-trip.
func (h *MetaHandler) GetMeta(w http.ResponseWriter, r *http.Request) {
	if h.keeper == nil {
		http.Error(w, "timings are not tracked", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(h.keeper.Snapshot()); err != nil {
		log.Printf("[meta] could not write the snapshot: %v", err)
	}
}
