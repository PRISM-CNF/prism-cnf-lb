package vnf

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"nse-ovs-lb/internal"
)

func TestRenderStaticOneToOneCommands(t *testing.T) {
	t.Parallel()

	zone := connectionTrackingZone("conn-1")

	commands := renderStaticOneToOneCommands(
		FlowInfo{
			ConnectionID:     "conn-1",
			IngressInterface: "if-in",
			EgressInterface:  "if-out",
		},
		&internal.LoadBalancerPolicy{
			Mode:      internal.LoadBalancerModeStaticOneToOne,
			VIP:       mustIPv4Addr(t, "172.16.4.10"),
			BackendIP: mustIPv4Addr(t, "172.16.4.100"),
		},
	)

	if len(commands) != 15 {
		t.Fatalf("command count = %d, want 15", len(commands))
	}

	if got := strings.Join(commands[0], " "); !strings.Contains(got, "dev if-in ingress chain 0 pref 100") || !strings.Contains(got, "ip_proto tcp") || !strings.Contains(got, "dst_ip 172.16.4.10") || !strings.Contains(got, fmt.Sprintf("action ct zone %d pipe action goto chain 10", zone)) {
		t.Fatalf("unexpected forward tcp lookup command: %s", got)
	}
	if got := strings.Join(commands[1], " "); !strings.Contains(got, "dev if-in ingress chain 10 pref 110") || !strings.Contains(got, "ct_state +trk+new") || !strings.Contains(got, fmt.Sprintf("action ct commit zone %d mark 1 nat dst addr 172.16.4.100 port 1-65535", zone)) {
		t.Fatalf("unexpected forward tcp commit command: %s", got)
	}
	if got := strings.Join(commands[4], " "); !strings.Contains(got, "dev if-out ingress chain 20 pref 210") || !strings.Contains(got, "src_ip 172.16.4.100") || !strings.Contains(got, "ct_state +trk+rpl") || !strings.Contains(got, fmt.Sprintf("action ct nat zone %d", zone)) {
		t.Fatalf("unexpected reverse tcp translate command: %s", got)
	}
}

func TestApplyStaticOneToOnePolicyLifecycle(t *testing.T) {
	t.Parallel()

	runner := &recordingTCRunner{}
	flow := FlowInfo{
		ConnectionID:     "conn-1",
		IngressInterface: "if-in",
		EgressInterface:  "if-out",
	}
	policy := &internal.LoadBalancerPolicy{
		Mode:      internal.LoadBalancerModeStaticOneToOne,
		VIP:       mustIPv4Addr(t, "172.16.4.10"),
		BackendIP: mustIPv4Addr(t, "172.16.4.100"),
		Hash:      "abcd1234",
	}
	session := &watchedRuleSession{
		runner: runner,
		options: Options{
			RulesPath:        "/etc/nse-lb/rules.yaml",
			RulePollInterval: defaultRulePollInterval,
			TCBinary:         defaultTCBinary,
		},
		flow:  flow,
		state: newSessionState(),
	}

	if err := session.applyPolicy(context.Background(), policy); err != nil {
		t.Fatalf("applyPolicy() error = %v", err)
	}

	if len(runner.commands) != 19 {
		t.Fatalf("command count after apply = %d, want 19", len(runner.commands))
	}
	if got, ok := session.state.Load(StateKeyLBMode); !ok || got != string(internal.LoadBalancerModeStaticOneToOne) {
		t.Fatalf("state lb mode = %v, %v; want %q, true", got, ok, internal.LoadBalancerModeStaticOneToOne)
	}
	if got, ok := session.state.Load(StateKeyLBVIP); !ok || got != "172.16.4.10" {
		t.Fatalf("state lb vip = %v, %v; want 172.16.4.10, true", got, ok)
	}
	if got, ok := session.state.Load(StateKeyLBBackendIP); !ok || got != "172.16.4.100" {
		t.Fatalf("state lb backend = %v, %v; want 172.16.4.100, true", got, ok)
	}

	if err := cleanupTCState(context.Background(), runner, flow); err != nil {
		t.Fatalf("cleanupTCState() error = %v", err)
	}
	if len(runner.commands) != 21 {
		t.Fatalf("command count after cleanup = %d, want 21", len(runner.commands))
	}
}

func TestCleanupTCStateIgnoresMissingClsactQdisc(t *testing.T) {
	t.Parallel()

	runner := &missingQdiscTCRunner{}
	flow := FlowInfo{
		IngressInterface: "if-in",
		EgressInterface:  "if-out",
	}

	if err := cleanupTCState(context.Background(), runner, flow); err != nil {
		t.Fatalf("cleanupTCState() error = %v", err)
	}
	if len(runner.commands) != 2 {
		t.Fatalf("command count after cleanup = %d, want 2", len(runner.commands))
	}
}

func mustIPv4Addr(t *testing.T, value string) netip.Addr {
	t.Helper()

	addr, err := netip.ParseAddr(value)
	if err != nil {
		t.Fatalf("ParseAddr() error = %v", err)
	}
	if !addr.Is4() {
		t.Fatalf("addr %s is not IPv4", value)
	}
	return addr
}

type recordingTCRunner struct {
	commands [][]string
}

func (r *recordingTCRunner) Run(_ context.Context, args ...string) error {
	copied := append([]string(nil), args...)
	r.commands = append(r.commands, copied)
	return nil
}

type missingQdiscTCRunner struct {
	commands [][]string
}

func (r *missingQdiscTCRunner) Run(_ context.Context, args ...string) error {
	copied := append([]string(nil), args...)
	r.commands = append(r.commands, copied)
	return fmt.Errorf(
		"tc %s: exit status 2: Error: Cannot find specified qdisc on specified device.",
		strings.Join(args, " "),
	)
}
