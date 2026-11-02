package processor

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"syscall"

	"github.com/vishvananda/netlink"

	"github.com/networkservicemesh/sdk/pkg/tools/log"
)

func ensureLinuxBridge(ctx context.Context, runtime *Runtime, bridgeState *passthroughState) error {
	if runtime == nil {
		return fmt.Errorf("runtime is required")
	}
	if runtime.IngressLink == nil {
		return fmt.Errorf("ingress link is required")
	}
	if runtime.EgressLink == nil {
		return fmt.Errorf("egress link is required")
	}

	logger := log.FromContext(ctx)
	logger.Infof(
		"passthrough bridge %s ensuring links for ingress=%s egress=%s",
		bridgeState.Name,
		bridgeState.IngressInterface,
		bridgeState.EgressInterface,
	)

	br, err := ensureBridgeLink(bridgeState.Name)
	if err != nil {
		return err
	}

	ingressLink := runtime.IngressLink
	egressLink := runtime.EgressLink

	mtu := ingressLink.Attrs().MTU
	if egressLink.Attrs().MTU > 0 && (mtu == 0 || egressLink.Attrs().MTU < mtu) {
		mtu = egressLink.Attrs().MTU
	}
	if mtu > 0 && br.Attrs().MTU != mtu {
		if err := netlink.LinkSetMTU(br, mtu); err != nil {
			return fmt.Errorf("set bridge MTU on %s: %w", bridgeState.Name, err)
		}
	}

	if err := prepareBridgeMember(ingressLink); err != nil {
		return err
	}
	if err := prepareBridgeMember(egressLink); err != nil {
		return err
	}

	if err := netlink.LinkSetUp(br); err != nil {
		return fmt.Errorf("set bridge %s up: %w", bridgeState.Name, err)
	}
	if err := setBridgeMaster(ingressLink, br); err != nil {
		return err
	}
	if err := setBridgeMaster(egressLink, br); err != nil {
		return err
	}
	if err := netlink.LinkSetUp(ingressLink); err != nil {
		return fmt.Errorf("set link %s up: %w", bridgeState.IngressInterface, err)
	}
	if err := netlink.LinkSetUp(egressLink); err != nil {
		return fmt.Errorf("set link %s up: %w", bridgeState.EgressInterface, err)
	}

	logger.Infof(
		"passthrough bridge %s ready with ingress=%s egress=%s mtu=%d",
		bridgeState.Name,
		bridgeState.IngressInterface,
		bridgeState.EgressInterface,
		br.Attrs().MTU,
	)

	return nil
}

func ensureBridgeLink(name string) (*netlink.Bridge, error) {
	link, err := netlink.LinkByName(name)
	if err == nil {
		br, ok := link.(*netlink.Bridge)
		if !ok {
			return nil, fmt.Errorf("%s exists but is not a bridge", name)
		}
		return br, nil
	}

	br := &netlink.Bridge{
		LinkAttrs: netlink.LinkAttrs{
			Name: name,
		},
	}
	if err := netlink.LinkAdd(br); err != nil {
		link, lookupErr := netlink.LinkByName(name)
		if lookupErr == nil {
			existing, ok := link.(*netlink.Bridge)
			if ok {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("create bridge %s: %w", name, err)
	}

	link, err = netlink.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("lookup bridge %s after create: %w", name, err)
	}
	existing, ok := link.(*netlink.Bridge)
	if !ok {
		return nil, fmt.Errorf("%s was created but is not a bridge", name)
	}
	return existing, nil
}

func prepareBridgeMember(link netlink.Link) error {
	if err := flushIPv4Addrs(link); err != nil {
		return err
	}
	if err := flushIPv4Routes(link); err != nil {
		return err
	}
	return nil
}

func setBridgeMaster(link netlink.Link, br *netlink.Bridge) error {
	if link.Attrs().MasterIndex == br.Attrs().Index {
		return nil
	}
	if link.Attrs().MasterIndex != 0 {
		if err := netlink.LinkSetNoMaster(link); err != nil {
			return fmt.Errorf("detach %s from existing master: %w", link.Attrs().Name, err)
		}
	}
	if err := netlink.LinkSetMaster(link, br); err != nil {
		return fmt.Errorf("attach %s to bridge %s: %w", link.Attrs().Name, br.Attrs().Name, err)
	}
	return nil
}

func flushIPv4Addrs(link netlink.Link) error {
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("list IPv4 addrs for %s: %w", link.Attrs().Name, err)
	}
	for _, addr := range addrs {
		if err := netlink.AddrDel(link, &addr); err != nil {
			return fmt.Errorf("remove addr %s from %s: %w", addr.IPNet, link.Attrs().Name, err)
		}
	}
	return nil
}

func flushIPv4Routes(link netlink.Link) error {
	routes, err := netlink.RouteList(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("list IPv4 routes for %s: %w", link.Attrs().Name, err)
	}
	for _, route := range routes {
		if route.LinkIndex != link.Attrs().Index {
			continue
		}
		if err := netlink.RouteDel(&route); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				continue
			}
			return fmt.Errorf("remove route on %s: %w", link.Attrs().Name, err)
		}
	}
	return nil
}

func removeBridge(bridgeState *passthroughState) error {
	if bridgeState == nil {
		return nil
	}
	link, err := netlink.LinkByName(bridgeState.Name)
	if err != nil {
		return nil
	}
	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("delete bridge %s: %w", bridgeState.Name, err)
	}
	return nil
}

func bridgeName(connectionID string) string {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(connectionID))
	return fmt.Sprintf("nsmbr%x", hasher.Sum32())
}
