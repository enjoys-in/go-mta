package ippool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/enjoys-in/go-mta/pkg/cache"
	"github.com/enjoys-in/go-mta/pkg/logger"
	"github.com/enjoys-in/go-mta/pkg/resolver"
)

const (
	cacheKeyAll    = "gomta:ips:all"
	cacheKeyPrefix = "gomta:ips:src:"
	cacheTTL       = 24 * time.Hour
)

// Source represents a single IP source entry from ips.toml.
type Source struct {
	Name       string `toml:"name"`
	IP         string `toml:"ip"`
	Weight     int    `toml:"weight"`
	EHLODomain string `toml:"ehlo_domain"`
	Warmup     bool   `toml:"warmup"`
}

// File is the TOML structure of ips.toml.
type File struct {
	Sources []Source `toml:"sources"`
}

// Pool manages IP sources loaded from TOML and cached in Dragonfly.
type Pool struct {
	sources []Source
	cache   cache.Cache
	log     *logger.Logger
}

// LoadFromFile reads ips.toml and returns a Pool.
func LoadFromFile(path string, c cache.Cache) (*Pool, error) {
	var f File
	if _, err := toml.DecodeFile(path, &f); err != nil {
		return nil, fmt.Errorf("ippool: load %s: %w", path, err)
	}

	// Apply defaults.
	for i := range f.Sources {
		if f.Sources[i].Weight <= 0 {
			f.Sources[i].Weight = 1
		}
	}

	return &Pool{
		sources: f.Sources,
		cache:   c,
		log:     logger.New("ippool"),
	}, nil
}

// WarmCache writes all IP sources into Dragonfly on startup.
func (p *Pool) WarmCache(ctx context.Context) error {
	if len(p.sources) == 0 {
		p.log.Warn("no IP sources to cache")
		return nil
	}

	// Store full list as JSON.
	data, err := json.Marshal(p.sources)
	if err != nil {
		return fmt.Errorf("ippool: marshal: %w", err)
	}
	if err := p.cache.Set(ctx, cacheKeyAll, string(data), cacheTTL); err != nil {
		return fmt.Errorf("ippool: cache set all: %w", err)
	}

	// Store each source individually.
	for _, src := range p.sources {
		d, _ := json.Marshal(src)
		key := cacheKeyPrefix + src.Name
		if err := p.cache.Set(ctx, key, string(d), cacheTTL); err != nil {
			p.log.Error("cache set source", err, slog.String("name", src.Name))
		}
	}

	p.log.Info("IP pool cached",
		slog.Int("count", len(p.sources)),
	)
	return nil
}

// IPs returns a flat list of IP addresses (respecting weight for rotation).
func (p *Pool) IPs() []string {
	var ips []string
	for _, src := range p.sources {
		for w := 0; w < src.Weight; w++ {
			ips = append(ips, src.IP)
		}
	}
	return ips
}

// IPv4IPs returns a weighted list of only IPv4 addresses.
func (p *Pool) IPv4IPs() []string {
	var ips []string
	for _, src := range p.sources {
		if !resolver.IsIPv4(src.IP) {
			continue
		}
		for w := 0; w < src.Weight; w++ {
			ips = append(ips, src.IP)
		}
	}
	return ips
}

// IPv6IPs returns a weighted list of only IPv6 addresses.
func (p *Pool) IPv6IPs() []string {
	var ips []string
	for _, src := range p.sources {
		if !resolver.IsIPv6(src.IP) {
			continue
		}
		for w := 0; w < src.Weight; w++ {
			ips = append(ips, src.IP)
		}
	}
	return ips
}

// ActiveIPs returns only non-warmup IPs.
func (p *Pool) ActiveIPs() []string {
	var ips []string
	for _, src := range p.sources {
		if src.Warmup {
			continue
		}
		for w := 0; w < src.Weight; w++ {
			ips = append(ips, src.IP)
		}
	}
	return ips
}

// Sources returns the raw source entries.
func (p *Pool) Sources() []Source {
	cp := make([]Source, len(p.sources))
	copy(cp, p.sources)
	return cp
}

// EHLOForIP returns the EHLO domain associated with the given IP, or "" if not found.
func (p *Pool) EHLOForIP(ip string) string {
	for _, src := range p.sources {
		if src.IP == ip {
			return src.EHLODomain
		}
	}
	return ""
}

// Count returns the number of configured sources.
func (p *Pool) Count() int {
	return len(p.sources)
}

// LoadFromCache reads the IP pool from Dragonfly (for worker nodes that don't have the TOML).
func LoadFromCache(ctx context.Context, c cache.Cache) (*Pool, error) {
	data, err := c.Get(ctx, cacheKeyAll)
	if err != nil {
		return nil, fmt.Errorf("ippool: cache get: %w", err)
	}

	var sources []Source
	if err := json.Unmarshal([]byte(data), &sources); err != nil {
		return nil, fmt.Errorf("ippool: unmarshal: %w", err)
	}

	return &Pool{
		sources: sources,
		cache:   c,
		log:     logger.New("ippool"),
	}, nil
}

// DetectSystemIPs discovers the system's non-loopback network interfaces
// and returns them classified as IPv4 and IPv6. Used in error messages to
// guide the user on which IPs to add to ips.toml.
func DetectSystemIPs() (ipv4, ipv6 []string, err error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, fmt.Errorf("ippool: list interfaces: %w", err)
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if resolver.IsIPv4(ip.String()) {
				ipv4 = append(ipv4, ip.String())
			} else {
				ipv6 = append(ipv6, ip.String())
			}
		}
	}
	return
}

// Describe returns a human-readable summary of all sources with their IP family.
func (p *Pool) Describe() string {
	if len(p.sources) == 0 {
		return "no IP sources configured"
	}
	var s string
	for _, src := range p.sources {
		family := "IPv4"
		if resolver.IsIPv6(src.IP) {
			family = "IPv6"
		}
		s += fmt.Sprintf("  %-15s  %-6s  weight=%d  warmup=%v  ehlo=%s\n",
			src.IP, family, src.Weight, src.Warmup, src.EHLODomain)
	}
	return s
}
