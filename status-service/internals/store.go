package internals

import (
	"sync"

	"github.com/goravaa/chotu/status"
)

type StatusStore struct {
	mu     sync.RWMutex
	status status.Status
}

func (s *StatusStore) Get() status.Status {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.status
}

func (s *StatusStore) Update(status status.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.status = status
}
