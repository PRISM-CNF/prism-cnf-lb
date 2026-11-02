package vnf

import (
	"context"

	"nse-ovs-lb/internal/processor"
)

func DefaultRuntimeHook() RuntimeHook {
	return RuntimeHookFunc(startTrafficProcessing)
}

// startTrafficProcessing is the second customization point for future VNFs.
// Use it for long-lived goroutines, user-space processors, or telemetry hooks.
// Read rule metadata or resource handles from sessionCtx.State() when needed.
func startTrafficProcessing(_ context.Context, _ *SessionContext) (processor.Session, error) {
	return processor.CloseFunc(func(context.Context) error {
		return nil
	}), nil
}
