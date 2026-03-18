package resolver

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
)

// Redis key layout:
//   HSET  gomta:mx:<domain>  <host>  <pref>
//   SADD  gomta:mx:domains   <domain> <domain> ...
//
// The HSET stores each MX host as a field with its preference as the value.
// The SET tracks all domains that have been cached, so the cron refresher
// can iterate all of them efficiently.

const (
	mxKeyPrefix  = "gomta:mx:"
	mxDomainSet  = "gomta:mx:domains"
	mxCacheTTL   = 12 * time.Hour
	mxRefreshTTL = 12 * time.Hour
)

// storeMXInCache writes MX records for a domain into Dragonfly using HSET.
// Also adds the domain to the global domain set for cron refresh.
func (r *Resolver) storeMXInCache(ctx context.Context, domain string, mxs []*net.MX) {
	key := mxKeyPrefix + domain

	// Build HSET field/value pairs: host1, pref1, host2, pref2, ...
	args := make([]any, 0, len(mxs)*2)
	for _, mx := range mxs {
		args = append(args, strings.TrimSuffix(mx.Host, "."), strconv.Itoa(int(mx.Pref)))
	}

	if err := r.cache.HSet(ctx, key, args...); err != nil {
		r.log.Error("failed to cache MX records in Dragonfly", err,
			slog.String("domain", domain),
			slog.Int("mx_count", len(mxs)),
		)
		return
	}

	// Set TTL on the hash key.
	if err := r.cache.Expire(ctx, key, mxCacheTTL); err != nil {
		r.log.Warn("failed to set TTL on cached MX records",
			slog.String("domain", domain),
			slog.Any("error", err),
		)
	}

	// Track this domain in the global set for refresh.
	if err := r.cache.SAdd(ctx, mxDomainSet, domain); err != nil {
		r.log.Warn("failed to add domain to MX tracking set",
			slog.String("domain", domain),
			slog.Any("error", err),
		)
	}

	r.log.Info("MX records cached in Dragonfly",
		slog.String("domain", domain),
		slog.Int("mx_count", len(mxs)),
		slog.String("cache_key", key),
		slog.Duration("ttl", mxCacheTTL),
	)
}

// loadMXFromCache reads cached MX records for a domain from Dragonfly HSET.
func (r *Resolver) loadMXFromCache(ctx context.Context, domain string) ([]*net.MX, error) {
	key := mxKeyPrefix + domain

	fields, err := r.cache.HGetAll(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("cache HGetAll %s: %w", key, err)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("cache miss: no MX records for %s", domain)
	}

	mxs := make([]*net.MX, 0, len(fields))
	for host, prefStr := range fields {
		pref, _ := strconv.Atoi(prefStr)
		mxs = append(mxs, &net.MX{Host: host, Pref: uint16(pref)})
	}

	return mxs, nil
}

// StartRefreshCron starts a background goroutine that refreshes all cached
// MX records every 12 hours by re-querying DNS for each tracked domain.
// The goroutine exits when ctx is cancelled.
func (r *Resolver) StartRefreshCron(ctx context.Context) {
	if r.cache == nil {
		r.log.Warn("MX cache refresh cron skipped — no cache configured")
		return
	}

	r.log.Info("starting MX cache refresh cron",
		slog.Duration("interval", mxRefreshTTL),
	)

	go func() {
		ticker := time.NewTicker(mxRefreshTTL)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				r.log.Info("MX cache refresh cron stopped — context cancelled")
				return
			case <-ticker.C:
				r.refreshAllDomains(ctx)
			}
		}
	}()
}

// refreshAllDomains re-resolves MX records for every domain in the tracking set.
func (r *Resolver) refreshAllDomains(ctx context.Context) {
	r.log.Info("MX cache refresh cron triggered — refreshing all cached domains")

	domains, err := r.cache.SMembers(ctx, mxDomainSet)
	if err != nil {
		r.log.Error("failed to read domain tracking set for MX refresh", err)
		return
	}

	if len(domains) == 0 {
		r.log.Info("MX cache refresh: no domains to refresh")
		return
	}

	r.log.Info("MX cache refresh: starting batch DNS re-resolution",
		slog.Int("domain_count", len(domains)),
	)

	refreshed := 0
	failed := 0

	for _, domain := range domains {
		lookupCtx, cancel := context.WithTimeout(ctx, r.timeout)
		res := &net.Resolver{}
		mxs, err := res.LookupMX(lookupCtx, domain)
		cancel()

		if err != nil || len(mxs) == 0 {
			r.log.Warn("MX refresh DNS lookup failed, keeping stale cache entry",
				slog.String("domain", domain),
				slog.Any("error", err),
			)
			failed++
			continue
		}

		r.storeMXInCache(ctx, domain, mxs)
		refreshed++

		r.log.Debug("MX refresh: domain updated successfully",
			slog.String("domain", domain),
			slog.Int("mx_count", len(mxs)),
			slog.String("primary_mx", mxs[0].Host),
		)
	}

	r.log.Info("MX cache refresh cron completed",
		slog.Int("total_domains", len(domains)),
		slog.Int("refreshed", refreshed),
		slog.Int("failed", failed),
	)
}
