package resolver

import (
	"net"
	"time"

	"github.com/enjoys-in/go-mta/pkg/cache"
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

// Resolver looks up MX hosts and resolves them to IPs using the configured strategy.
// All MX lookups are cached in Dragonfly via HSET for LRU-style immediate reuse.
type Resolver struct {
	strategy Strategy
	timeout  time.Duration
	cache    cache.Cache // nil = no caching
	log      *logger.Logger
}

// New creates a Resolver with the given strategy.
// Pass nil for c to disable MX caching.
func New(strategy Strategy, timeout time.Duration, c cache.Cache) *Resolver {
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	return &Resolver{
		strategy: strategy,
		timeout:  timeout,
		cache:    c,
		log:      logger.New("resolver"),
	}
}

// Strategy returns the current strategy name.
func (r *Resolver) Strategy() Strategy {
	return r.strategy
}
