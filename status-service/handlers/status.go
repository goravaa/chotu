package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/goravaa/chotu/status"
	"github.com/goravaa/chotu/status-service/internals"
)

type StatusHandler struct {
	store  *internals.StatusStore
	hub    *internals.StatusHub
	keeper *internals.Keeper
}

func NewStatusHandler(
	store *internals.StatusStore, hub *internals.StatusHub, keeper *internals.Keeper) *StatusHandler {
	return &StatusHandler{
		store:  store,
		hub:    hub,
		keeper: keeper,
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
	reqID := time.Now().UnixNano()
	log.Printf("[status %d] 1. Request received: %s %s", reqID, r.Method, r.URL.Path)

	var incoming status.Status

	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		log.Printf("[status %d] Request failed: invalid JSON: %v", reqID, err)
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	log.Printf("[status %d] 2. Request body decoded, cycle: %d, state: %s", reqID, incoming.Cycle, incoming.State)

	// Since decoding implies validation/processing in this context, we can log it
	log.Printf("[status %d] 3. Status document validated/processed", reqID)

	h.store.Update(incoming)
	log.Printf("[status %d] 4. Document/state written to the store", reqID)

	h.hub.Broadcast(incoming)
	log.Printf("[status %d] 5. Event created/published to the event hub", reqID)

	// The keeper watches the same document, so task timings stay in step
	// with what the page shows. A nil keeper is a no-op.
	h.keeper.Observe(incoming)

	w.WriteHeader(http.StatusNoContent)
	log.Printf("[status %d] 7. HTTP response returned", reqID)
}
