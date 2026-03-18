package domainpool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/enjoys-in/go-mta/pkg/cache"
	"github.com/enjoys-in/go-mta/pkg/logger"
)

const (
	cacheKeyAll    = "gomta:domains:all"
	cacheKeyPrefix = "gomta:domains:rule:"
	cacheTTL       = 24 * time.Hour
)

// WarmupStep defines a warmup schedule entry.
type WarmupStep struct {
	Day int `toml:"day" json:"day"`
	Max int `toml:"max" json:"max"`
}

// Warmup holds warmup configuration.
type Warmup struct {
	Enabled  bool         `toml:"enabled" json:"enabled"`
	Schedule []WarmupStep `toml:"schedule" json:"schedule"`
}

// Limits holds domain-specific delivery limits.
type Limits struct {
	MaxConcurrentConns int    `toml:"max_concurrent_conns" json:"max_concurrent_conns"`
	MaxMsgsPerConn     int    `toml:"max_msgs_per_conn" json:"max_msgs_per_conn"`
	MsgsPerMinute      int    `toml:"msgs_per_minute" json:"msgs_per_minute"`
	MaxDailyPerIP      int    `toml:"max_daily_per_ip" json:"max_daily_per_ip"`
	ConnectionTimeout  string `toml:"connection_timeout" json:"connection_timeout"`
	GreetDelay         string `toml:"greet_delay" json:"greet_delay"`
	RetryOn421         bool   `toml:"retry_on_421" json:"retry_on_421"`
	RetryBackoffSec    int    `toml:"retry_backoff_sec" json:"retry_backoff_sec"`
}

// Rule represents a domain-specific delivery rule from domains.toml.
type Rule struct {
	Domain                     string `toml:"domain" json:"domain"`
	ConnectionLimit            int    `toml:"connection_limit" json:"connection_limit"`
	MaxDeliveriesPerConnection int    `toml:"max_deliveries_per_connection" json:"max_deliveries_per_connection"`
	MaxMessageRate             string `toml:"max_message_rate" json:"max_message_rate"`
	IPPool                     string `toml:"ip_pool" json:"ip_pool"`
	Warmup                     Warmup `toml:"warmup" json:"warmup"`
	Limits                     Limits `toml:"limits" json:"limits"`
}

// File is the TOML structure of domains.toml.
type File struct {
	Domains map[string]Rule `toml:"domains"`
}

// Pool manages domain rules loaded from TOML and cached in Dragonfly.
type Pool struct {
	rules map[string]Rule // keyed by the unique identifier
	cache cache.Cache
	log   *logger.Logger
}

// LoadFromFile reads domains.toml and returns a Pool.
func LoadFromFile(path string, c cache.Cache) (*Pool, error) {
	var f File
	if _, err := toml.DecodeFile(path, &f); err != nil {
		return nil, fmt.Errorf("domainpool: load %s: %w", path, err)
	}

	return &Pool{
		rules: f.Domains,
		cache: c,
		log:   logger.New("domainpool"),
	}, nil
}

// WarmCache writes all domain rules into Dragonfly on startup.
func (p *Pool) WarmCache(ctx context.Context) error {
	if len(p.rules) == 0 {
		p.log.Warn("no domain rules to cache")
		return nil
	}

	// Store full map as JSON.
	data, err := json.Marshal(p.rules)
	if err != nil {
		return fmt.Errorf("domainpool: marshal: %w", err)
	}
	if err := p.cache.Set(ctx, cacheKeyAll, string(data), cacheTTL); err != nil {
		return fmt.Errorf("domainpool: cache set all: %w", err)
	}

	// Store each rule individually.
	for name, rule := range p.rules {
		d, _ := json.Marshal(rule)
		key := cacheKeyPrefix + name
		if err := p.cache.Set(ctx, key, string(d), cacheTTL); err != nil {
			p.log.Error("cache set rule", err, slog.String("name", name))
		}
	}

	p.log.Info("domain pool cached",
		slog.Int("count", len(p.rules)),
	)
	return nil
}

// Match finds the most specific rule for a recipient domain.
// Priority: exact match > suffix match (e.g. ".gmail.com") > wildcard ("*").
func (p *Pool) Match(domain string) *Rule {
	domain = strings.ToLower(domain)

	// 1. Exact match.
	for _, rule := range p.rules {
		if strings.ToLower(rule.Domain) == domain {
			r := rule
			return &r
		}
	}

	// 2. Suffix match (e.g. ".gmail.com" matches "user@gmail.com").
	var bestSuffix *Rule
	bestLen := 0
	for _, rule := range p.rules {
		pat := strings.ToLower(rule.Domain)
		if strings.HasPrefix(pat, ".") {
			// ".gmail.com" should match "gmail.com" and "sub.gmail.com"
			suffix := pat[1:] // "gmail.com"
			if domain == suffix || strings.HasSuffix(domain, pat) {
				if len(pat) > bestLen {
					bestLen = len(pat)
					r := rule
					bestSuffix = &r
				}
			}
		}
	}
	if bestSuffix != nil {
		return bestSuffix
	}

	// 3. Wildcard default.
	for _, rule := range p.rules {
		if rule.Domain == "*" {
			r := rule
			return &r
		}
	}

	return nil
}

// Rules returns all loaded rules.
func (p *Pool) Rules() map[string]Rule {
	cp := make(map[string]Rule, len(p.rules))
	for k, v := range p.rules {
		cp[k] = v
	}
	return cp
}

// Count returns the number of rules.
func (p *Pool) Count() int {
	return len(p.rules)
}

// LoadFromCache reads domain rules from Dragonfly.
func LoadFromCache(ctx context.Context, c cache.Cache) (*Pool, error) {
	data, err := c.Get(ctx, cacheKeyAll)
	if err != nil {
		return nil, fmt.Errorf("domainpool: cache get: %w", err)
	}

	var rules map[string]Rule
	if err := json.Unmarshal([]byte(data), &rules); err != nil {
		return nil, fmt.Errorf("domainpool: unmarshal: %w", err)
	}

	return &Pool{
		rules: rules,
		cache: c,
		log:   logger.New("domainpool"),
	}, nil
}
