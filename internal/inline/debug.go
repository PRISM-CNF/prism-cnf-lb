package inline

import (
	"fmt"
	"sort"
	"strings"

	"github.com/networkservicemesh/api/pkg/api/networkservice"
	kernelmech "github.com/networkservicemesh/api/pkg/api/networkservice/mechanisms/kernel"
)

func connectionSummary(conn *networkservice.Connection) string {
	if conn == nil {
		return "<nil>"
	}

	path := conn.GetPath()
	pathIndex := 0
	pathLen := 0
	segments := []string{}
	if path != nil {
		pathIndex = int(path.GetIndex())
		pathLen = len(path.GetPathSegments())
		for _, segment := range path.GetPathSegments() {
			segments = append(segments, segment.GetName())
		}
	}

	mechanismType := ""
	interfaceName := ""
	if conn.GetMechanism() != nil {
		mechanismType = conn.GetMechanism().GetType()
	}
	if mechanism := kernelmech.ToMechanism(conn.GetMechanism()); mechanism != nil {
		interfaceName = mechanism.GetInterfaceName()
	}

	return fmt.Sprintf(
		"id=%s state=%s path=%d/%d iface=%s mech=%s endpoint=%s labels=%s segments=%s",
		conn.GetId(),
		conn.GetState().String(),
		pathIndex,
		pathLen,
		interfaceName,
		mechanismType,
		conn.GetNetworkServiceEndpointName(),
		mapSummary(conn.GetLabels()),
		strings.Join(segments, ">"),
	)
}

func requestSummary(request *networkservice.NetworkServiceRequest) string {
	if request == nil {
		return "<nil>"
	}

	return fmt.Sprintf(
		"conn={%s} prefs=%s",
		connectionSummary(request.GetConnection()),
		mechanismListSummary(request.GetMechanismPreferences()),
	)
}

func mechanismListSummary(mechanisms []*networkservice.Mechanism) string {
	if len(mechanisms) == 0 {
		return "[]"
	}

	items := make([]string, 0, len(mechanisms))
	for _, mechanism := range mechanisms {
		items = append(items, mechanismSummary(mechanism))
	}
	return "[" + strings.Join(items, ";") + "]"
}

func mechanismSummary(mechanism *networkservice.Mechanism) string {
	if mechanism == nil {
		return "<nil>"
	}

	interfaceName := ""
	if kernel := kernelmech.ToMechanism(mechanism); kernel != nil {
		interfaceName = kernel.GetInterfaceName()
	}

	return fmt.Sprintf(
		"type=%s iface=%s params=%s",
		mechanism.GetType(),
		interfaceName,
		mapSummary(mechanism.GetParameters()),
	)
}

func mapSummary(values map[string]string) string {
	if len(values) == 0 {
		return "{}"
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%s", key, values[key]))
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

func trimDownstreamPath(conn *networkservice.Connection) bool {
	if conn == nil || conn.GetPath() == nil {
		return false
	}

	keep := int(conn.GetPath().GetIndex()) + 1
	if keep <= 0 || keep >= len(conn.GetPath().GetPathSegments()) {
		return false
	}

	conn.Path.PathSegments = append([]*networkservice.PathSegment(nil), conn.GetPath().GetPathSegments()[:keep]...)
	return true
}
