package resolver

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
)

// LookupMX returns the MX hosts for a domain, sorted by preference.
// Uses the Dragonfly cache (HSET per domain) for immediate LRU-style lookups.
// On cache miss, performs a live DNS query and stores the result.
func (r *Resolver) LookupMX(ctx context.Context, domain string) ([]*net.MX, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))

	r.log.Debug("MX lookup requested",
		slog.String("domain", domain),
		slog.String("strategy", string(r.strategy)),
	)

	// Try cache first.
	if r.cache != nil {
		mxs, err := r.loadMXFromCache(ctx, domain)
		if err == nil && len(mxs) > 0 {
			r.log.Info("MX cache hit — returning cached MX records",
				slog.String("domain", domain),
				slog.Int("mx_count", len(mxs)),
			)
			return mxs, nil
		}
		if err != nil {
			r.log.Debug("MX cache miss or error, will query DNS",
				slog.String("domain", domain),
				slog.String("reason", err.Error()),
			)
		}
	}

	// Cache miss — live DNS lookup.
	r.log.Info("performing live DNS MX lookup",
		slog.String("domain", domain),
	)

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	res := &net.Resolver{}
	mxs, err := res.LookupMX(ctx, domain)
	if err != nil || len(mxs) == 0 {
		r.log.Warn("DNS MX lookup returned no records, falling back to domain as host",
			slog.String("domain", domain),
			slog.Any("error", err),
		)
		fallback := []*net.MX{{Host: domain, Pref: 0}}
		if r.cache != nil {
			r.storeMXInCache(ctx, domain, fallback)
		}
		return fallback, nil
	}

	r.log.Info("DNS MX lookup succeeded",
		slog.String("domain", domain),
		slog.Int("mx_count", len(mxs)),
		slog.String("primary_mx", mxs[0].Host),
		slog.Int("primary_pref", int(mxs[0].Pref)),
	)

	// Store in cache for future lookups.
	if r.cache != nil {
		r.storeMXInCache(ctx, domain, mxs)
	}

	return mxs, nil
}

// ResolveCompatible resolves a hostname to IPs that are compatible with the
// given source IP address family. This prevents errors when an IPv4 source
// tries to connect to an IPv6-only MX server.
func (r *Resolver) ResolveCompatible(ctx context.Context, host, sourceIP string) (ips []string, unbind bool, err error) {
	sourceIsV4 := IsIPv4(sourceIP)

	preferred := "ip4"
	fallback := "ip6"
	if !sourceIsV4 {
		preferred = "ip6"
		fallback = "ip4"
	}

	r.log.Debug("resolving host with family compatibility check",
		slog.String("host", host),
		slog.String("source_ip", sourceIP),
		slog.Bool("source_is_v4", sourceIsV4),
		slog.String("preferred_network", preferred),
	)

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	res := &net.Resolver{}

	ips, err = lookupIP(ctx, res, preferred, host)
	if err == nil && len(ips) > 0 {
		r.log.Debug("resolved host using preferred address family",
			slog.String("host", host),
			slog.String("family", preferred),
			slog.Int("ip_count", len(ips)),
		)
		return ips, false, nil
	}

	r.log.Warn("no compatible IPs found for source, trying fallback family",
		slog.String("host", host),
		slog.String("source_ip", sourceIP),
		slog.String("preferred", preferred),
		slog.String("fallback", fallback),
		slog.Any("preferred_error", err),
	)

	ips, err = lookupIP(ctx, res, fallback, host)
	if err != nil {
		r.log.Error("both address families failed for host resolution", err,
			slog.String("host", host),
			slog.String("preferred", preferred),
			slog.String("fallback", fallback),
		)
		return nil, false, fmt.Errorf("resolver: both %s and %s failed for %s: %w", preferred, fallback, host, err)
	}

	r.log.Warn("using fallback family — caller should unbind from source IP",
		slog.String("host", host),
		slog.String("family_used", fallback),
		slog.Int("ip_count", len(ips)),
	)
	return ips, true, nil
}

// ResolveHost resolves a hostname to IP addresses using the configured strategy.
func (r *Resolver) ResolveHost(ctx context.Context, host string) ([]string, error) {
	r.log.Debug("resolving host IPs",
		slog.String("host", host),
		slog.String("strategy", string(r.strategy)),
	)

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	res := &net.Resolver{}
	var ips []string
	var err error

	switch r.strategy {
	case Ipv4Only:
		ips, err = lookupIP(ctx, res, "ip4", host)
	case Ipv6Only:
		ips, err = lookupIP(ctx, res, "ip6", host)
	case Ipv4AndIpv6:
		ips, err = r.resolveBoth(ctx, res, host)
	case Ipv4ThenIpv6:
		ips, err = r.resolveWithFallback(ctx, res, host, "ip4", "ip6")
	case Ipv6ThenIpv4:
		ips, err = r.resolveWithFallback(ctx, res, host, "ip6", "ip4")
	default:
		ips, err = lookupIP(ctx, res, "ip4", host)
	}

	if err != nil {
		r.log.Warn("host IP resolution failed",
			slog.String("host", host),
			slog.String("strategy", string(r.strategy)),
			slog.Any("error", err),
		)
	} else {
		r.log.Debug("host IP resolution succeeded",
			slog.String("host", host),
			slog.Int("ip_count", len(ips)),
		)
	}
	return ips, err
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

	r.log.Debug("primary family failed, trying fallback",
		slog.String("host", host),
		slog.String("primary", primary),
		slog.String("fallback", fallback),
	)

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
