package resolver

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/enjoys-in/go-mta/pkg/logger"
)

// Strategy defines how MX IP addresses are resolved.
type Strategy string

const (
	Ipv4AndIpv6  Strategy = "Ipv4AndIpv6"
	Ipv4Only     Strategy = "Ipv4Only"
	Ipv6Only     Strategy = "Ipv6Only"
	Ipv4ThenIpv6 Strategy = "Ipv4ThenIpv6"
	Ipv6ThenIpv4 Strategy = "Ipv6ThenIpv4"
)

// ParseStrategy converts a string to a Strategy, defaulting to Ipv4Only.
func ParseStrategy(s string) Strategy {
	switch Strategy(s) {
	case Ipv4AndIpv6, Ipv4Only, Ipv6Only, Ipv4ThenIpv6, Ipv6ThenIpv4:
		return Strategy(s)
	default:
		return Ipv4Only
	}
}

// IsIPv4 returns true if the address is an IPv4 address.
func IsIPv4(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return parsed.To4() != nil
}

// IsIPv6 returns true if the address is an IPv6 address (and not IPv4-mapped).
func IsIPv6(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return parsed.To4() == nil
}

// Resolver looks up MX hosts and resolves them to IPs using the configured strategy.
type Resolver struct {
	strategy Strategy
	timeout  time.Duration
	log      *logger.Logger
}

// New creates a Resolver with the given strategy.
func New(strategy Strategy, timeout time.Duration) *Resolver {
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	return &Resolver{
		strategy: strategy,
		timeout:  timeout,
		log:      logger.New("resolver"),
	}
}

// LookupMX returns the MX hosts for a domain, sorted by preference.
func (r *Resolver) LookupMX(ctx context.Context, domain string) ([]*net.MX, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	res := &net.Resolver{}
	mxs, err := res.LookupMX(ctx, domain)
	if err != nil || len(mxs) == 0 {
		return []*net.MX{{Host: domain, Pref: 0}}, nil
	}
	return mxs, nil
}

// ResolveCompatible resolves a hostname to IPs that are compatible with the
// given source IP address family. This prevents errors like:
//
//	"dial tcp: address [2407:...]:0: no suitable address found"
//
// when an IPv4 source tries to connect to an IPv6-only MX server.
//
// Logic:
//   - If sourceIP is IPv4, only return IPv4 MX addresses.
//   - If sourceIP is IPv6, only return IPv6 MX addresses.
//   - If no compatible addresses found, fall back to the other family
//     and signal the caller to skip binding (return unbind=true).
func (r *Resolver) ResolveCompatible(ctx context.Context, host, sourceIP string) (ips []string, unbind bool, err error) {
	sourceIsV4 := IsIPv4(sourceIP)

	// Determine preferred and fallback networks.
	preferred := "ip4"
	fallback := "ip6"
	if !sourceIsV4 {
		preferred = "ip6"
		fallback = "ip4"
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	res := &net.Resolver{}

	// Try preferred family first.
	ips, err = lookupIP(ctx, res, preferred, host)
	if err == nil && len(ips) > 0 {
		return ips, false, nil
	}

	// Preferred failed, try fallback. Caller should NOT bind to sourceIP
	// because the address families don't match.
	r.log.Warn("no compatible IPs for source, falling back",
		slog.String("host", host),
		slog.String("source_ip", sourceIP),
		slog.String("preferred", preferred),
		slog.String("fallback", fallback),
	)

	ips, err = lookupIP(ctx, res, fallback, host)
	if err != nil {
		return nil, false, fmt.Errorf("resolver: both %s and %s failed for %s: %w", preferred, fallback, host, err)
	}
	return ips, true, nil // unbind=true: don't bind to sourceIP
}

// ResolveHost resolves a hostname to IP addresses using the configured strategy.
// Returns only addresses compatible with the strategy, preventing
// errors like "dial tcp: address [2407:...]:0: no suitable address found".
func (r *Resolver) ResolveHost(ctx context.Context, host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	res := &net.Resolver{}

	switch r.strategy {
	case Ipv4Only:
		return lookupIP(ctx, res, "ip4", host)

	case Ipv6Only:
		return lookupIP(ctx, res, "ip6", host)

	case Ipv4AndIpv6:
		return r.resolveBoth(ctx, res, host)

	case Ipv4ThenIpv6:
		return r.resolveWithFallback(ctx, res, host, "ip4", "ip6")

	case Ipv6ThenIpv4:
		return r.resolveWithFallback(ctx, res, host, "ip6", "ip4")

	default:
		return lookupIP(ctx, res, "ip4", host)
	}
}

// resolveBoth resolves A and AAAA concurrently, merges results.
func (r *Resolver) resolveBoth(ctx context.Context, res *net.Resolver, host string) ([]string, error) {
	type result struct {
		ips []string
		err error
	}
	ch := make(chan result, 2)

	go func() {
		ips, err := lookupIP(ctx, res, "ip4", host)
		ch <- result{ips, err}
	}()
	go func() {
		ips, err := lookupIP(ctx, res, "ip6", host)
		ch <- result{ips, err}
	}()

	var all []string
	var lastErr error
	for i := 0; i < 2; i++ {
		r := <-ch
		if r.err != nil {
			lastErr = r.err
			continue
		}
		all = append(all, r.ips...)
	}

	if len(all) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return all, nil
}

// resolveWithFallback tries primary network first, falls back to secondary.
func (r *Resolver) resolveWithFallback(ctx context.Context, res *net.Resolver, host, primary, fallback string) ([]string, error) {
	ips, err := lookupIP(ctx, res, primary, host)
	if err == nil && len(ips) > 0 {
		return ips, nil
	}

	ips, err = lookupIP(ctx, res, fallback, host)
	if err != nil {
		return nil, fmt.Errorf("resolver: both %s and %s failed for %s: %w", primary, fallback, host, err)
	}
	return ips, nil
}

func lookupIP(ctx context.Context, res *net.Resolver, network, host string) ([]string, error) {
	host = strings.TrimSuffix(host, ".")
	ips, err := res.LookupIP(ctx, network, host)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(ips))
	for _, ip := range ips {
		result = append(result, ip.String())
	}
	return result, nil
}

// Strategy returns the current strategy name.
func (r *Resolver) Strategy() Strategy {
	return r.strategy
}

// ClassifyIPs partitions a list of IPs into IPv4 and IPv6 buckets.
func ClassifyIPs(ips []string) (ipv4, ipv6 []string) {
	for _, ip := range ips {
		if IsIPv4(ip) {
			ipv4 = append(ipv4, ip)
		} else {
			ipv6 = append(ipv6, ip)
		}
	}
	return
}
