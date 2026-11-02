package vnf

import (
	"context"
	"fmt"

	"nse-ovs-lb/internal/processor"
)

type Handler interface {
	Configure(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error)
}

type HandlerFunc func(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error)

func (f HandlerFunc) Configure(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error) {
	if f == nil {
		return nil, fmt.Errorf("handler func is required")
	}
	return f(ctx, sessionCtx)
}

type RuleInstaller interface {
	Install(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error)
}

type RuleInstallerFunc func(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error)

func (f RuleInstallerFunc) Install(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error) {
	if f == nil {
		return nil, fmt.Errorf("rule installer func is required")
	}
	return f(ctx, sessionCtx)
}

type RuntimeHook interface {
	Start(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error)
}

type RuntimeHookFunc func(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error)

func (f RuntimeHookFunc) Start(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error) {
	if f == nil {
		return nil, fmt.Errorf("runtime hook func is required")
	}
	return f(ctx, sessionCtx)
}

type CleanupHook interface {
	Stop(ctx context.Context, sessionCtx *SessionContext) error
}

type CleanupHookFunc func(ctx context.Context, sessionCtx *SessionContext) error

func (f CleanupHookFunc) Stop(ctx context.Context, sessionCtx *SessionContext) error {
	if f == nil {
		return fmt.Errorf("cleanup hook func is required")
	}
	return f(ctx, sessionCtx)
}

type phasedHandler struct {
	rules   RuleInstaller
	runtime RuntimeHook
	cleanup CleanupHook
}

func NewNoopHandler() Handler {
	return NewPhasedHandler(
		DefaultRuleInstaller(Options{}),
		DefaultRuntimeHook(),
		DefaultCleanupHook(),
	)
}

func NewPhasedHandler(rules RuleInstaller, runtime RuntimeHook, cleanup CleanupHook) Handler {
	return &phasedHandler{
		rules:   rules,
		runtime: runtime,
		cleanup: cleanup,
	}
}

func (h *phasedHandler) Configure(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error) {
	if h == nil {
		return nil, fmt.Errorf("handler is required")
	}

	rules := h.rules
	if rules == nil {
		rules = DefaultRuleInstaller(Options{})
	}

	runtimeHook := h.runtime
	if runtimeHook == nil {
		runtimeHook = DefaultRuntimeHook()
	}

	cleanup := h.cleanup
	if cleanup == nil {
		cleanup = DefaultCleanupHook()
	}

	rulesSession, err := rules.Install(ctx, sessionCtx)
	if err != nil {
		return nil, err
	}

	runtimeSession, err := runtimeHook.Start(ctx, sessionCtx)
	if err != nil {
		if rulesSession != nil {
			_ = rulesSession.Close(ctx)
		}
		return nil, err
	}

	cleanupSession := processor.CloseFunc(func(closeCtx context.Context) error {
		return cleanup.Stop(closeCtx, sessionCtx)
	})

	// Close order is runtime hook, explicit cleanup hook, then installed rules.
	return processor.JoinSessions(rulesSession, cleanupSession, runtimeSession), nil
}
