package vnf

import (
	"fmt"

	"github.com/vishvananda/netlink"
	"nse-ovs-lb/internal/processor"
)

type FlowInfo struct {
	ConnectionID     string
	IngressInterface string
	EgressInterface  string
}

type SessionContext struct {
	flow    FlowInfo
	runtime *processor.Runtime
	state   *SessionState
}

func newSessionContext(runtime *processor.Runtime) (*SessionContext, error) {
	if runtime == nil {
		return nil, fmt.Errorf("runtime is required")
	}
	if runtime.Flow == nil {
		return nil, fmt.Errorf("runtime flow is required")
	}

	return &SessionContext{
		flow: FlowInfo{
			ConnectionID:     runtime.Flow.ConnectionID,
			IngressInterface: runtime.Flow.IngressInterface,
			EgressInterface:  runtime.Flow.EgressInterface,
		},
		runtime: runtime,
		state:   newSessionState(),
	}, nil
}

func (c *SessionContext) Flow() FlowInfo {
	if c == nil {
		return FlowInfo{}
	}
	return c.flow
}

func (c *SessionContext) ConnectionID() string {
	return c.Flow().ConnectionID
}

func (c *SessionContext) IngressInterface() string {
	return c.Flow().IngressInterface
}

func (c *SessionContext) EgressInterface() string {
	return c.Flow().EgressInterface
}

func (c *SessionContext) IngressLink() netlink.Link {
	if c == nil || c.runtime == nil {
		return nil
	}
	return c.runtime.IngressLink
}

func (c *SessionContext) EgressLink() netlink.Link {
	if c == nil || c.runtime == nil {
		return nil
	}
	return c.runtime.EgressLink
}

func (c *SessionContext) State() *SessionState {
	if c == nil {
		return nil
	}
	return c.state
}
