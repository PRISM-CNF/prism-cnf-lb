package vnf

import (
	"context"
	"fmt"

	"nse-ovs-lb/internal/processor"
)

type Dataplane interface {
	Connect(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error)
}

type DataplaneFunc func(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error)

func (f DataplaneFunc) Connect(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error) {
	if f == nil {
		return nil, fmt.Errorf("dataplane func is required")
	}
	return f(ctx, sessionCtx)
}

type passthroughDataplane struct{}

func NewPassthroughDataplane() Dataplane {
	return &passthroughDataplane{}
}

func (d *passthroughDataplane) Connect(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error) {
	if sessionCtx == nil {
		return nil, fmt.Errorf("session context is required")
	}

	session, err := processor.NewPassthroughSetup().Setup(ctx, sessionCtx.runtime)
	if err != nil {
		return nil, err
	}

	if state := sessionCtx.State(); state != nil {
		state.Store(StateKeyDataplaneKind, "linux-bridge")
		state.Store(StateKeyDataplaneMode, "passthrough")
	}

	return session, nil
}
