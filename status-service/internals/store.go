package internals

import "sync"

type StatusStore struct {
	mu     sync.RWMutex
	status Status
}

func (s *StatusStore) Get() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.status
}

func (s *StatusStore) Update(status Status) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.status = status
}
