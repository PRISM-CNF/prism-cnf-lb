package internal

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type LoadBalancerMode string

const (
	LoadBalancerModeStaticOneToOne LoadBalancerMode = "static_one_to_one"
	LoadBalancerModeEmpty          LoadBalancerMode = "empty"
)

type LoadBalancerPolicy struct {
	Mode      LoadBalancerMode
	VIP       netip.Addr
	BackendIP netip.Addr
	Hash      string
}

type rawLoadBalancerPolicy struct {
	Mode      string `yaml:"mode"`
	VIP       string `yaml:"vip"`
	BackendIP string `yaml:"backend_ip"`
}

func LoadLoadBalancerPolicyFromFile(path string) (*LoadBalancerPolicy, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("lb rules path is required")
	}

	rawData, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read lb rules file: %w", err)
	}

	return ParseLoadBalancerPolicy(rawData)
}

func ParseLoadBalancerPolicy(rawData []byte) (*LoadBalancerPolicy, error) {
	var raw rawLoadBalancerPolicy
	if err := yaml.Unmarshal(rawData, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal lb rules YAML: %w", err)
	}

	mode, err := normalizeLoadBalancerMode(raw.Mode)
	if err != nil {
		return nil, err
	}

	policy := &LoadBalancerPolicy{
		Mode: mode,
		Hash: hashBytes(rawData),
	}
	if mode == LoadBalancerModeEmpty {
		return policy, nil
	}

	vip, err := parseIPv4Address(raw.VIP, "vip")
	if err != nil {
		return nil, err
	}
	backendIP, err := parseIPv4Address(raw.BackendIP, "backend_ip")
	if err != nil {
		return nil, err
	}
	if vip == backendIP {
		return nil, fmt.Errorf("vip and backend_ip must differ")
	}

	policy.VIP = vip
	policy.BackendIP = backendIP
	return policy, nil
}

func normalizeLoadBalancerMode(value string) (LoadBalancerMode, error) {
	switch LoadBalancerMode(strings.ToLower(strings.TrimSpace(value))) {
	case LoadBalancerModeStaticOneToOne:
		return LoadBalancerModeStaticOneToOne, nil
	case LoadBalancerModeEmpty:
		return LoadBalancerModeEmpty, nil
	default:
		return "", fmt.Errorf("invalid lb mode %q", value)
	}
}

func parseIPv4Address(raw string, fieldName string) (netip.Addr, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return netip.Addr{}, fmt.Errorf("%s must not be empty", fieldName)
	}

	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parse %s %q: %w", fieldName, raw, err)
	}
	if !addr.Is4() {
		return netip.Addr{}, fmt.Errorf("%s must be an IPv4 address", fieldName)
	}
	return addr, nil
}

func hashBytes(rawData []byte) string {
	sum := sha256.Sum256(rawData)
	return hex.EncodeToString(sum[:])
}
