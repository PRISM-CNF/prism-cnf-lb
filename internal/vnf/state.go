package vnf

import "sync"

type SessionState struct {
	mu     sync.RWMutex
	values map[string]any
}

func newSessionState() *SessionState {
	return &SessionState{
		values: map[string]any{},
	}
}

func (s *SessionState) Store(key string, value any) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
}

func (s *SessionState) Load(key string) (any, bool) {
	if s == nil {
		return nil, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.values[key]
	return value, ok
}

func (s *SessionState) Delete(key string) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
}
