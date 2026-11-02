package processor

import (
	"context"
	"errors"
)

type CloseFunc func(ctx context.Context) error

func (f CloseFunc) Close(ctx context.Context) error {
	if f == nil {
		return nil
	}
	return f(ctx)
}

type compositeSession struct {
	sessions []Session
}

func JoinSessions(sessions ...Session) Session {
	filtered := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		if session == nil {
			continue
		}
		filtered = append(filtered, session)
	}
	return &compositeSession{sessions: filtered}
}

func (s *compositeSession) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}

	var errs []error
	for i := len(s.sessions) - 1; i >= 0; i-- {
		if err := s.sessions[i].Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
