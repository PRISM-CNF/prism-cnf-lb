package vnf

import "context"

func DefaultCleanupHook() CleanupHook {
	return CleanupHookFunc(stopTrafficProcessing)
}

// stopTrafficProcessing is the final customization point for future VNFs.
// Use it to release state that should be torn down before rule removal.
// Read and delete entries from sessionCtx.State() to clean up external resources.
func stopTrafficProcessing(_ context.Context, _ *SessionContext) error {
	return nil
}
