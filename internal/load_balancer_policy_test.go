package internal

import "testing"

func TestParseLoadBalancerPolicyStaticOneToOne(t *testing.T) {
	t.Parallel()

	policy, err := ParseLoadBalancerPolicy([]byte(`
mode: static_one_to_one
vip: 172.16.4.10
backend_ip: 172.16.4.100
`))
	if err != nil {
		t.Fatalf("ParseLoadBalancerPolicy() error = %v", err)
	}

	if policy.Mode != LoadBalancerModeStaticOneToOne {
		t.Fatalf("mode = %q, want %q", policy.Mode, LoadBalancerModeStaticOneToOne)
	}
	if got := policy.VIP.String(); got != "172.16.4.10" {
		t.Fatalf("vip = %q, want 172.16.4.10", got)
	}
	if got := policy.BackendIP.String(); got != "172.16.4.100" {
		t.Fatalf("backend_ip = %q, want 172.16.4.100", got)
	}
	if policy.Hash == "" {
		t.Fatal("hash is empty")
	}
}

func TestParseLoadBalancerPolicyEmpty(t *testing.T) {
	t.Parallel()

	policy, err := ParseLoadBalancerPolicy([]byte("mode: empty\n"))
	if err != nil {
		t.Fatalf("ParseLoadBalancerPolicy() error = %v", err)
	}

	if policy.Mode != LoadBalancerModeEmpty {
		t.Fatalf("mode = %q, want %q", policy.Mode, LoadBalancerModeEmpty)
	}
	if policy.VIP.IsValid() {
		t.Fatalf("vip should be invalid for empty mode, got %s", policy.VIP)
	}
	if policy.BackendIP.IsValid() {
		t.Fatalf("backend_ip should be invalid for empty mode, got %s", policy.BackendIP)
	}
}

func TestParseLoadBalancerPolicyRejectsNonIPv4(t *testing.T) {
	t.Parallel()

	_, err := ParseLoadBalancerPolicy([]byte(`
mode: static_one_to_one
vip: 2001:db8::10
backend_ip: 172.16.4.100
`))
	if err == nil {
		t.Fatal("expected error for non-IPv4 vip")
	}
}
