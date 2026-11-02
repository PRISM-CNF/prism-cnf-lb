package processor

import (
	"context"
	"fmt"
)

type passthroughSetup struct{}

type passthroughState struct {
	Name             string
	IngressInterface string
	EgressInterface  string
}

type passthroughSession struct {
	state *passthroughState
}

func NewPassthroughSetup() FlowSetup {
	return &passthroughSetup{}
}

func NewPassthroughProcessor() Processor {
	return New(NewPassthroughSetup())
}

func (p *passthroughSetup) Setup(ctx context.Context, runtime *Runtime) (Session, error) {
	if runtime == nil || runtime.Flow == nil {
		return nil, fmt.Errorf("runtime is required")
	}

	state := &passthroughState{
		Name:             bridgeName(runtime.Flow.ConnectionID),
		IngressInterface: runtime.Flow.IngressInterface,
		EgressInterface:  runtime.Flow.EgressInterface,
	}

	if err := ensureLinuxBridge(ctx, runtime, state); err != nil {
		return nil, err
	}

	return &passthroughSession{state: state}, nil
}

func (s *passthroughSession) Close(ctx context.Context) error {
	return removeBridge(s.state)
}
