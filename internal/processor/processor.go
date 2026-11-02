package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/networkservicemesh/api/pkg/api/networkservice"
	"github.com/vishvananda/netlink"
)

const (
	linkWaitTimeout  = 5 * time.Second
	linkPollInterval = 100 * time.Millisecond
)

type Flow struct {
	ConnectionID         string
	IngressInterface     string
	EgressInterface      string
	UpstreamConnection   *networkservice.Connection
	DownstreamConnection *networkservice.Connection
}

func (f *Flow) Clone() *Flow {
	if f == nil {
		return nil
	}

	cloned := *f
	cloned.UpstreamConnection = cloneConnection(f.UpstreamConnection)
	cloned.DownstreamConnection = cloneConnection(f.DownstreamConnection)
	return &cloned
}

type Runtime struct {
	Flow        *Flow
	IngressLink netlink.Link
	EgressLink  netlink.Link
}

type Session interface {
	Close(ctx context.Context) error
}

// FlowSetup is the main extension point for concrete inline VNFs.
// It runs after NSM has created both local interfaces inside the middle pod.
type FlowSetup interface {
	Setup(ctx context.Context, runtime *Runtime) (Session, error)
}

type SetupFunc func(ctx context.Context, runtime *Runtime) (Session, error)

func (f SetupFunc) Setup(ctx context.Context, runtime *Runtime) (Session, error) {
	if f == nil {
		return nil, fmt.Errorf("flow setup func is required")
	}
	return f(ctx, runtime)
}

type Processor interface {
	Activate(ctx context.Context, flow *Flow) (Session, error)
}

type flowProcessor struct {
	setup FlowSetup
}

func New(setup FlowSetup) Processor {
	return &flowProcessor{setup: setup}
}

func (p *flowProcessor) Activate(ctx context.Context, flow *Flow) (Session, error) {
	if flow == nil {
		return nil, fmt.Errorf("flow is required")
	}
	if p.setup == nil {
		return nil, fmt.Errorf("flow setup is required")
	}

	runtime, err := prepareRuntime(ctx, flow)
	if err != nil {
		if linkPending(err) {
			return nil, Pending(err)
		}
		return nil, err
	}

	return p.setup.Setup(ctx, runtime)
}

type pendingError struct {
	err error
}

func (e *pendingError) Error() string {
	if e == nil || e.err == nil {
		return "processor activation is pending"
	}
	return e.err.Error()
}

func (e *pendingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func Pending(err error) error {
	if err == nil {
		return nil
	}
	return &pendingError{err: err}
}

func IsPending(err error) bool {
	var pending *pendingError
	return errors.As(err, &pending)
}

func prepareRuntime(ctx context.Context, flow *Flow) (*Runtime, error) {
	if flow.IngressInterface == "" {
		return nil, fmt.Errorf("ingress interface is required")
	}
	if flow.EgressInterface == "" {
		return nil, fmt.Errorf("egress interface is required")
	}

	ingressLink, err := waitForLink(ctx, flow.IngressInterface)
	if err != nil {
		return nil, err
	}
	egressLink, err := waitForLink(ctx, flow.EgressInterface)
	if err != nil {
		return nil, err
	}

	return &Runtime{
		Flow:        flow.Clone(),
		IngressLink: ingressLink,
		EgressLink:  egressLink,
	}, nil
}

func cloneConnection(conn *networkservice.Connection) *networkservice.Connection {
	if conn == nil {
		return nil
	}
	return conn.Clone()
}

func waitForLink(ctx context.Context, name string) (netlink.Link, error) {
	deadline := time.Now().Add(linkWaitTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		link, err := netlink.LinkByName(name)
		if err == nil {
			return link, nil
		}
		lastErr = err
		time.Sleep(linkPollInterval)
	}
	return nil, fmt.Errorf("wait for link %s: %w", name, lastErr)
}

func linkPending(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "link not found")
}
