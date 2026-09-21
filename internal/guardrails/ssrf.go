package guardrails

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	NetworkPublicHTTPS = "public_https"
	NetworkPrivate     = "private_network"
	NetworkLoopback    = "loopback"
)

var forbiddenHostnames = map[string]struct{}{
	"metadata.google.internal": {}, "metadata.google": {}, "instance-data": {},
}

func ValidateDestination(ctx context.Context, rawURL, mode string, resolver *net.Resolver) (*url.URL, []net.IP, error) {
	parsed, mode, err := ValidateDestinationSyntax(rawURL, mode)
	if err != nil {
		return nil, nil, err
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addresses, err := resolver.LookupIP(lookupCtx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, nil, fmt.Errorf("guardrail hostname resolution failed: %w", err)
	}
	for _, address := range addresses {
		if err := validateIPForMode(address, mode); err != nil {
			return nil, nil, err
		}
	}
	return parsed, addresses, nil
}

// ValidateDestinationSyntax applies the non-network portion of the outbound
// URL policy. Admin configuration uses it before enablement; dispatch repeats
// the full DNS-aware validation immediately before dialing.
func ValidateDestinationSyntax(rawURL, mode string) (*url.URL, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, "", errors.New("invalid guardrail URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, "", errors.New("guardrail URL must use http or https")
	}
	if parsed.User != nil {
		return nil, "", errors.New("guardrail URL must not contain user information")
	}
	if parsed.Hostname() == "" {
		return nil, "", errors.New("guardrail URL requires a hostname")
	}
	if parsed.Fragment != "" {
		return nil, "", errors.New("guardrail URL must not contain a fragment")
	}
	for key := range parsed.Query() {
		if isSecretFieldName(key) {
			return nil, "", errors.New("guardrail URL must not contain secret query parameters")
		}
	}
	if mode == "" {
		mode = NetworkPublicHTTPS
	}
	if mode != NetworkPublicHTTPS && mode != NetworkPrivate && mode != NetworkLoopback {
		return nil, "", errors.New("invalid network access mode")
	}
	if mode == NetworkPublicHTTPS && parsed.Scheme != "https" {
		return nil, "", errors.New("public guardrails require HTTPS")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if _, blocked := forbiddenHostnames[host]; blocked {
		return nil, "", errors.New("metadata destinations are forbidden")
	}
	if strings.HasSuffix(host, ".internal") && strings.Contains(host, "metadata") {
		return nil, "", errors.New("metadata destinations are forbidden")
	}
	if literal := net.ParseIP(host); literal != nil {
		if err := validateIPForMode(literal, mode); err != nil {
			return nil, "", err
		}
	}
	return parsed, mode, nil
}

func validateIPForMode(ip net.IP, mode string) error {
	if ip == nil {
		return errors.New("invalid guardrail address")
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		ip = ipv4
	}
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() {
		return errors.New("link-local, metadata, multicast, and unspecified destinations are forbidden")
	}
	// The standard library private check includes RFC1918 and IPv6 ULA.
	private := ip.IsPrivate()
	loopback := ip.IsLoopback()
	switch mode {
	case NetworkPublicHTTPS:
		if private || loopback {
			return errors.New("public guardrails require a publicly routable destination")
		}
	case NetworkPrivate:
		if loopback {
			return errors.New("loopback destinations require loopback network mode")
		}
	case NetworkLoopback:
		if !loopback {
			return errors.New("loopback mode permits only loopback destinations")
		}
	default:
		return errors.New("invalid network access mode")
	}
	return nil
}

func secureDialContext(mode string, resolver *net.Resolver, dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := resolver.LookupIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("guardrail hostname resolution failed: %w", err)
		}
		for _, ip := range ips {
			if err := validateIPForMode(ip, mode); err != nil {
				return nil, err
			}
		}
		var lastErr error
		for _, ip := range ips {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}
