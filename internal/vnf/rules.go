package vnf

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/networkservicemesh/sdk/pkg/tools/log"

	"nse-ovs-lb/internal"
	"nse-ovs-lb/internal/processor"
)

const (
	tcForwardChain        = 10
	tcReverseChain        = 20
	tcConnectionMark      = "1"
	tcConnectionMarkMatch = "1/0xffffffff"
	tcConnectionPortRange = "1-65535"

	tcPrefForwardLookupTCP       = 100
	tcPrefForwardCommitTCP       = 110
	tcPrefForwardEstablishedTCP  = 111
	tcPrefForwardLookupUDP       = 120
	tcPrefForwardCommitUDP       = 130
	tcPrefForwardEstablishedUDP  = 131
	tcPrefForwardLookupICMP      = 140
	tcPrefForwardCommitICMP      = 150
	tcPrefForwardEstablishedICMP = 151

	tcPrefReverseLookupTCP     = 200
	tcPrefReverseTranslateTCP  = 210
	tcPrefReverseLookupUDP     = 220
	tcPrefReverseTranslateUDP  = 230
	tcPrefReverseLookupICMP    = 240
	tcPrefReverseTranslateICMP = 250
)

type tcRunner interface {
	Run(ctx context.Context, args ...string) error
}

type execTCRunner struct {
	binary string
}

type tcProtocolRule struct {
	IPProto                string
	ForwardLookupPref      int
	ForwardCommitPref      int
	ForwardEstablishedPref int
	ReverseLookupPref      int
	ReverseTranslatePref   int
}

type watchedRuleSession struct {
	runner  tcRunner
	options Options
	flow    FlowInfo
	state   *SessionState
	logCtx  context.Context
	closeCh chan struct{}
	doneCh  chan struct{}

	closeOnce sync.Once
	mu        sync.Mutex
	active    *internal.LoadBalancerPolicy
}

func DefaultRuleInstaller(options Options) RuleInstaller {
	options = options.withDefaults()
	return RuleInstallerFunc(func(ctx context.Context, sessionCtx *SessionContext) (processor.Session, error) {
		return installTrafficRulesFromPath(ctx, sessionCtx, options, &execTCRunner{binary: options.TCBinary})
	})
}

func installTrafficRulesFromPath(
	ctx context.Context,
	sessionCtx *SessionContext,
	options Options,
	runner tcRunner,
) (processor.Session, error) {
	if sessionCtx == nil {
		return nil, fmt.Errorf("session context is required")
	}
	if runner == nil {
		return nil, fmt.Errorf("tc runner is required")
	}

	options = options.withDefaults()
	if err := options.validate(); err != nil {
		return nil, err
	}

	logCtx := log.WithLog(context.Background(), log.FromContext(ctx))
	session := &watchedRuleSession{
		runner:  runner,
		options: options,
		flow:    sessionCtx.Flow(),
		state:   sessionCtx.State(),
		logCtx:  logCtx,
		closeCh: make(chan struct{}),
		doneCh:  make(chan struct{}),
	}

	policy, err := session.loadPolicy()
	if err != nil {
		return nil, err
	}
	if err := session.applyPolicy(ctx, policy); err != nil {
		return nil, err
	}

	go session.watchPolicy()
	return session, nil
}

func (r *execTCRunner) Run(ctx context.Context, args ...string) error {
	if r == nil {
		return fmt.Errorf("tc runner is required")
	}
	if strings.TrimSpace(r.binary) == "" {
		return fmt.Errorf("tc binary is required")
	}

	output, err := exec.CommandContext(ctx, r.binary, args...).CombinedOutput()
	if err == nil {
		return nil
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return fmt.Errorf("%s %s: %w", r.binary, strings.Join(args, " "), err)
	}
	return fmt.Errorf("%s %s: %w: %s", r.binary, strings.Join(args, " "), err, trimmed)
}

func (s *watchedRuleSession) loadPolicy() (*internal.LoadBalancerPolicy, error) {
	policy, err := internal.LoadLoadBalancerPolicyFromFile(s.options.RulesPath)
	if err != nil {
		return nil, fmt.Errorf("load lb rules from %s: %w", s.options.RulesPath, err)
	}
	return policy, nil
}

func (s *watchedRuleSession) applyPolicy(ctx context.Context, policy *internal.LoadBalancerPolicy) error {
	if policy == nil {
		return fmt.Errorf("lb policy is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := cleanupTCState(ctx, s.runner, s.flow); err != nil {
		return err
	}
	if policy.Mode == internal.LoadBalancerModeStaticOneToOne {
		if err := applyStaticOneToOnePolicy(ctx, s.runner, s.flow, policy); err != nil {
			return err
		}
	}

	s.active = policy
	storePolicyState(s.state, s.options, policy)

	log.FromContext(ctx).Infof(
		"applied lb policy mode=%s vip=%s backend=%s hash=%s ct_zone=%d ingress=%s egress=%s",
		policy.Mode,
		describePolicyAddr(policy.VIP),
		describePolicyAddr(policy.BackendIP),
		shortHash(policy.Hash),
		connectionTrackingZone(s.flow.ConnectionID),
		s.flow.IngressInterface,
		s.flow.EgressInterface,
	)

	return nil
}

func (s *watchedRuleSession) watchPolicy() {
	ticker := time.NewTicker(s.options.RulePollInterval)
	defer ticker.Stop()
	defer close(s.doneCh)

	for {
		select {
		case <-s.closeCh:
			return
		case <-ticker.C:
			policy, err := s.loadPolicy()
			if err != nil {
				log.FromContext(s.logCtx).Errorf("reload lb rules failed: %v", err)
				continue
			}
			if s.samePolicyHash(policy.Hash) {
				continue
			}
			if err := s.applyPolicy(s.logCtx, policy); err != nil {
				log.FromContext(s.logCtx).Errorf("apply reloaded lb rules failed: %v", err)
			}
		}
	}
}

func (s *watchedRuleSession) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}

	s.closeOnce.Do(func() {
		close(s.closeCh)
	})
	<-s.doneCh

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != nil {
		s.state.Delete(StateKeyLBMode)
		s.state.Delete(StateKeyLBRuleHash)
		s.state.Delete(StateKeyLBRulesPath)
		s.state.Delete(StateKeyLBVIP)
		s.state.Delete(StateKeyLBBackendIP)
	}

	return cleanupTCState(ctx, s.runner, s.flow)
}

func (s *watchedRuleSession) samePolicyHash(hash string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.active != nil && s.active.Hash == hash
}

func applyStaticOneToOnePolicy(
	ctx context.Context,
	runner tcRunner,
	flow FlowInfo,
	policy *internal.LoadBalancerPolicy,
) error {
	if runner == nil {
		return fmt.Errorf("tc runner is required")
	}
	if policy == nil {
		return fmt.Errorf("lb policy is required")
	}
	if flow.IngressInterface == "" || flow.EgressInterface == "" {
		return fmt.Errorf("both ingress and egress interfaces are required")
	}

	for _, iface := range []string{flow.IngressInterface, flow.EgressInterface} {
		if err := ensureClsact(ctx, runner, iface); err != nil {
			return err
		}
	}

	for _, command := range renderStaticOneToOneCommands(flow, policy) {
		if err := runner.Run(ctx, command...); err != nil {
			return err
		}
	}

	return nil
}

func renderStaticOneToOneCommands(flow FlowInfo, policy *internal.LoadBalancerPolicy) [][]string {
	if policy == nil {
		return nil
	}

	protocols := []tcProtocolRule{
		{
			IPProto:                "tcp",
			ForwardLookupPref:      tcPrefForwardLookupTCP,
			ForwardCommitPref:      tcPrefForwardCommitTCP,
			ForwardEstablishedPref: tcPrefForwardEstablishedTCP,
			ReverseLookupPref:      tcPrefReverseLookupTCP,
			ReverseTranslatePref:   tcPrefReverseTranslateTCP,
		},
		{
			IPProto:                "udp",
			ForwardLookupPref:      tcPrefForwardLookupUDP,
			ForwardCommitPref:      tcPrefForwardCommitUDP,
			ForwardEstablishedPref: tcPrefForwardEstablishedUDP,
			ReverseLookupPref:      tcPrefReverseLookupUDP,
			ReverseTranslatePref:   tcPrefReverseTranslateUDP,
		},
		{
			IPProto:                "icmp",
			ForwardLookupPref:      tcPrefForwardLookupICMP,
			ForwardCommitPref:      tcPrefForwardCommitICMP,
			ForwardEstablishedPref: tcPrefForwardEstablishedICMP,
			ReverseLookupPref:      tcPrefReverseLookupICMP,
			ReverseTranslatePref:   tcPrefReverseTranslateICMP,
		},
	}
	zone := connectionTrackingZone(flow.ConnectionID)
	vip := policy.VIP.String()
	backend := policy.BackendIP.String()

	commands := make([][]string, 0, len(protocols)*5)
	for _, rule := range protocols {
		commands = append(commands,
			renderCTLookupCommand(flow.IngressInterface, tcForwardChain, rule.ForwardLookupPref, rule.IPProto, "dst_ip", vip, zone),
			renderCTCommitNATCommand(flow.IngressInterface, tcForwardChain, rule.ForwardCommitPref, rule.IPProto, "dst_ip", vip, zone, backend),
			renderCTTranslateCommand(flow.IngressInterface, tcForwardChain, rule.ForwardEstablishedPref, rule.IPProto, "", "", zone, "+trk+est"),
			renderCTLookupCommand(flow.EgressInterface, tcReverseChain, rule.ReverseLookupPref, rule.IPProto, "src_ip", backend, zone),
			renderCTTranslateCommand(flow.EgressInterface, tcReverseChain, rule.ReverseTranslatePref, rule.IPProto, "src_ip", backend, zone, "+trk+rpl"),
		)
	}
	return commands
}

func renderCTLookupCommand(iface string, chain, pref int, ipProto, matchField, matchValue string, zone int) []string {
	return []string{
		"filter", "replace",
		"dev", iface,
		"ingress",
		"chain", "0",
		"pref", fmt.Sprintf("%d", pref),
		"protocol", "ip",
		"flower", "skip_hw",
		"ip_proto", ipProto,
		matchField, matchValue,
		"ct_state", "-trk",
		"action", "ct", "zone", fmt.Sprintf("%d", zone),
		"pipe",
		"action", "goto", "chain", fmt.Sprintf("%d", chain),
	}
}

func renderCTCommitNATCommand(iface string, chain, pref int, ipProto, matchField, matchValue string, zone int, backend string) []string {
	return []string{
		"filter", "replace",
		"dev", iface,
		"ingress",
		"chain", fmt.Sprintf("%d", chain),
		"pref", fmt.Sprintf("%d", pref),
		"protocol", "ip",
		"flower", "skip_hw",
		"ip_proto", ipProto,
		matchField, matchValue,
		"ct_zone", fmt.Sprintf("%d", zone),
		"ct_state", "+trk+new",
		"action", "ct", "commit",
		"zone", fmt.Sprintf("%d", zone),
		"mark", tcConnectionMark,
		"nat", "dst", "addr", backend,
		"port", tcConnectionPortRange,
	}
}

func renderCTTranslateCommand(iface string, chain, pref int, ipProto, matchField, matchValue string, zone int, ctState string) []string {
	command := []string{
		"filter", "replace",
		"dev", iface,
		"ingress",
		"chain", fmt.Sprintf("%d", chain),
		"pref", fmt.Sprintf("%d", pref),
		"protocol", "ip",
		"flower", "skip_hw",
		"ip_proto", ipProto,
	}
	if strings.TrimSpace(matchField) != "" {
		command = append(command, matchField, matchValue)
	}
	command = append(
		command,
		"ct_zone", fmt.Sprintf("%d", zone),
		"ct_mark", tcConnectionMarkMatch,
		"ct_state", ctState,
		"action", "ct", "nat", "zone", fmt.Sprintf("%d", zone),
	)
	return command
}

func connectionTrackingZone(connectionID string) int {
	trimmed := strings.TrimSpace(connectionID)
	if trimmed == "" {
		return 1
	}

	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(trimmed))
	return int(hasher.Sum32()%65535) + 1
}

func ensureClsact(ctx context.Context, runner tcRunner, iface string) error {
	err := runner.Run(ctx, "qdisc", "add", "dev", iface, "clsact")
	if isExistingTCObjectError(err) {
		return nil
	}
	return err
}

func cleanupTCState(ctx context.Context, runner tcRunner, flow FlowInfo) error {
	if runner == nil {
		return fmt.Errorf("tc runner is required")
	}

	var errs []error
	for _, iface := range []string{flow.IngressInterface, flow.EgressInterface} {
		if strings.TrimSpace(iface) == "" {
			continue
		}
		err := runner.Run(ctx, "qdisc", "delete", "dev", iface, "clsact")
		if isMissingTCObjectError(err) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func storePolicyState(state *SessionState, options Options, policy *internal.LoadBalancerPolicy) {
	if state == nil || policy == nil {
		return
	}

	state.Store(StateKeyLBRulesPath, options.RulesPath)
	state.Store(StateKeyLBMode, string(policy.Mode))
	state.Store(StateKeyLBRuleHash, policy.Hash)

	if policy.Mode == internal.LoadBalancerModeStaticOneToOne {
		state.Store(StateKeyLBVIP, policy.VIP.String())
		state.Store(StateKeyLBBackendIP, policy.BackendIP.String())
		return
	}

	state.Delete(StateKeyLBVIP)
	state.Delete(StateKeyLBBackendIP)
}

func shortHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

func describePolicyAddr(addr fmt.Stringer) string {
	if addr == nil {
		return "-"
	}
	return addr.String()
}

func isExistingTCObjectError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "file exists")
}

func isMissingTCObjectError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such file or directory") ||
		strings.Contains(message, "cannot delete qdisc with handle of zero") ||
		strings.Contains(message, "cannot find specified qdisc on specified device")
}
