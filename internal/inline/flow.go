package inline

import (
	"fmt"
	"strings"

	"github.com/networkservicemesh/api/pkg/api/networkservice"
	kernelmech "github.com/networkservicemesh/api/pkg/api/networkservice/mechanisms/kernel"

	"nse-ovs-lb/internal/processor"
)

func flowForConnectionPath(conn *networkservice.Connection, downstreamConn *networkservice.Connection) (*processor.Flow, error) {
	if conn == nil {
		return nil, fmt.Errorf("connection is required")
	}
	if conn.GetPath() == nil {
		return nil, fmt.Errorf("connection path is required")
	}

	ingressInterface := pathServerInterface(conn)
	if ingressInterface == "" {
		ingressInterface = mechanismInterfaceName(conn)
	}
	if ingressInterface == "" {
		return nil, fmt.Errorf("ingress interface is unavailable in response path")
	}

	egressInterface := nextServerInterface(conn)
	if egressInterface == "" {
		egressInterface = mechanismInterfaceName(downstreamConn)
	}
	if egressInterface == "" {
		egressInterface = mechanismInterfaceName(conn)
	}
	if egressInterface == "" {
		return nil, fmt.Errorf("egress interface is unavailable in response path")
	}
	if ingressInterface == egressInterface {
		return nil, nil
	}

	return &processor.Flow{
		ConnectionID:         conn.GetId(),
		IngressInterface:     ingressInterface,
		EgressInterface:      egressInterface,
		UpstreamConnection:   cloneConnection(conn),
		DownstreamConnection: cloneConnection(downstreamConn),
	}, nil
}

func pathServerInterface(conn *networkservice.Connection) string {
	path := conn.GetPath()
	if path == nil {
		return ""
	}
	index := int(path.GetIndex())
	segments := path.GetPathSegments()
	if index < 0 || index >= len(segments) {
		return ""
	}
	return interfaceNameFromMetric(segments[index].GetMetrics()["server_interface"])
}

func nextServerInterface(conn *networkservice.Connection) string {
	path := conn.GetPath()
	if path == nil {
		return ""
	}
	index := int(path.GetIndex())
	segments := path.GetPathSegments()
	for i := index + 1; i < len(segments); i++ {
		if name := interfaceNameFromMetric(segments[i].GetMetrics()["server_interface"]); name != "" {
			return name
		}
	}
	return ""
}

func mechanismInterfaceName(conn *networkservice.Connection) string {
	if conn == nil {
		return ""
	}
	mechanism := kernelmech.ToMechanism(conn.GetMechanism())
	if mechanism == nil {
		return ""
	}
	return mechanism.GetInterfaceName()
}

func interfaceNameFromMetric(value string) string {
	if value == "" {
		return ""
	}
	_, name, found := strings.Cut(value, "/")
	if found {
		return name
	}
	return value
}

func cloneConnection(conn *networkservice.Connection) *networkservice.Connection {
	if conn == nil {
		return nil
	}
	return conn.Clone()
}
