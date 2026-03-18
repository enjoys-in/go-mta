package server

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	// Register all delivery adapters.
	_ "github.com/enjoys-in/go-mta/internal/mta/delivery/direct"
	_ "github.com/enjoys-in/go-mta/internal/mta/delivery/http"
	_ "github.com/enjoys-in/go-mta/internal/mta/delivery/relay"

	"github.com/enjoys-in/go-mta/pkg/bounce"
	"github.com/enjoys-in/go-mta/pkg/cache"
	"github.com/enjoys-in/go-mta/pkg/configloader"
	"github.com/enjoys-in/go-mta/pkg/domainpool"
	"github.com/enjoys-in/go-mta/pkg/events"
	"github.com/enjoys-in/go-mta/pkg/ippool"
	"github.com/enjoys-in/go-mta/pkg/logger"
	"github.com/enjoys-in/go-mta/pkg/preflight"
	"github.com/enjoys-in/go-mta/pkg/queue"
	"github.com/enjoys-in/go-mta/pkg/ratelimit"
	"github.com/enjoys-in/go-mta/pkg/resolver"
	"github.com/enjoys-in/go-mta/pkg/retry"
	"github.com/enjoys-in/go-mta/pkg/rotation"
	"github.com/enjoys-in/go-mta/pkg/types"
)

// Server is the main orchestrator that wraps the MTA delivery core
// with queue, retry, rate-limit, events, bounce detection, and logging.
type Server struct {
	cfg       *configloader.ServerConfig
	cache     *cache.DragonflyCache
	events    *events.Handler
	queue     *queue.Queue
	retrier   *retry.Retrier
	limiter   *ratelimit.Limiter
	rotator   *rotation.Rotator
	preflight *preflight.Checker
	bouncer   *bounce.Detector
	resolver  *resolver.Resolver
	ips       *ippool.Pool
	domains   *domainpool.Pool
	log       *logger.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New builds a fully wired Server from the given config.
func New(cfg *configloader.ServerConfig) (*Server, error) {
	// Cache
	c := cache.New(cache.Options{
		Addr:     cfg.CacheAddr,
		Username: cfg.CacheUser,
		Password: cfg.CachePassword,
		DB:       cfg.CacheDB,
	})

	// Resolver (IP lookup strategy) — receives cache for MX record caching.
	strategy := resolver.ParseStrategy(cfg.IPLookupStrategy)
	res := resolver.New(strategy, 0, c)

	// Load IP pool from TOML (if configured), cache in Dragonfly.
	var ips *ippool.Pool
	if cfg.IPsFile != "" {
		var err error
		ips, err = ippool.LoadFromFile(cfg.IPsFile, c)
		if err != nil {
			return nil, fmt.Errorf("server: load ips: %w", err)
		}
	}

	// Load domain rules from TOML (if configured), cache in Dragonfly.
	var domains *domainpool.Pool
	if cfg.DomainsFile != "" {
		var err error
		domains, err = domainpool.LoadFromFile(cfg.DomainsFile, c)
		if err != nil {
			return nil, fmt.Errorf("server: load domains: %w", err)
		}
	}

	// Build IP rotation list: prefer ippool sources, fallback to config LocalIPs.
	var rotIPs []string
	if ips != nil && ips.Count() > 0 {
		rotIPs = ips.ActiveIPs()
	}
	if len(rotIPs) == 0 {
		rotIPs = cfg.LocalIPs
	}
	if len(rotIPs) == 0 {
		// No IPs configured at all — detect system IPs and tell the user.
		sysV4, sysV6, _ := ippool.DetectSystemIPs()
		hint := "\n\n  No IP pool configured! Please add IPs to ips.toml.\n"
		if len(sysV4) > 0 || len(sysV6) > 0 {
			hint += "\n  Detected system IPs you can add:\n"
			for _, ip := range sysV4 {
				hint += fmt.Sprintf("    IPv4: %s\n", ip)
			}
			for _, ip := range sysV6 {
				hint += fmt.Sprintf("    IPv6: %s\n", ip)
			}
			hint += "\n  Example ips.toml entry:\n"
			hint += "    [[sources]]\n"
			hint += "    name = \"primary\"\n"
			if len(sysV4) > 0 {
				hint += fmt.Sprintf("    ip = \"%s\"\n", sysV4[0])
			} else {
				hint += fmt.Sprintf("    ip = \"%s\"\n", sysV6[0])
			}
			hint += "    weight = 1\n"
		} else {
			hint += "\n  Could not detect any usable network interfaces.\n"
		}
		return nil, fmt.Errorf("%s", hint)
	}
	rot, err := rotation.New(rotIPs)
	if err != nil {
		return nil, err
	}

	// Events
	eh := events.NewHandler(1024)

	// Retry
	rs := retry.Strategy{
		MaxAttempts: cfg.MaxRetries,
		BaseDelay:   5 * time.Second,
		MaxDelay:    10 * time.Minute,
		Multiplier:  2.0,
	}

	s := &Server{
		cfg:       cfg,
		cache:     c,
		events:    eh,
		retrier:   retry.New(rs),
		limiter:   ratelimit.New(c, int64(cfg.RateLimitPerSec), time.Second),
		rotator:   rot,
		preflight: preflight.New(cfg.MaxMessageSize, cfg.MaxRecipients),
		bouncer:   bounce.NewDetector(),
		resolver:  res,
		ips:       ips,
		domains:   domains,
		log:       logger.New(types.ComponentServer),
	}

	// Queue — worker func is the delivery pipeline (asynq-backed via Dragonfly/Redis).
	s.queue = queue.New(queue.RedisConfig{
		Addr:     cfg.CacheAddr,
		Username: cfg.CacheUser,
		Password: cfg.CachePassword,
		DB:       cfg.CacheDB,
	}, cfg.QueueWorkers, s.processJob)

	return s, nil
}

// Start launches the event dispatcher and queue workers.
// Warms IP and domain caches in Dragonfly concurrently on startup.
func (s *Server) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)

	// Warm caches concurrently — don't block startup.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		var wg sync.WaitGroup
		if s.ips != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.ips.WarmCache(ctx); err != nil {
					s.log.Error("failed to warm IP cache", err)
				}
			}()
		}
		if s.domains != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.domains.WarmCache(ctx); err != nil {
					s.log.Error("failed to warm domain cache", err)
				}
			}()
		}
		wg.Wait()
	}()

	go s.events.Dispatch()
	s.queue.Start(ctx)

	// Start the MX cache refresh cron (every 12 hours).
	s.resolver.StartRefreshCron(ctx)

	s.log.Info("server started",
		slog.String("method", s.cfg.Method),
		slog.Int("workers", s.cfg.QueueWorkers),
		slog.String("ip_lookup", string(s.resolver.Strategy())),
	)
}

// Shutdown gracefully stops the server, draining the queue and events.
func (s *Server) Shutdown(ctx context.Context) {
	s.events.Emit(events.Event{
		Type:      types.EventShutdownStart,
		Timestamp: time.Now(),
	})
	s.log.Info("shutting down...")

	s.wg.Wait()

	s.queue.Stop()
	s.events.Stop()
	s.cache.Close()

	if s.cancel != nil {
		s.cancel()
	}

	s.log.Info("shutdown complete")
}

// Events returns the event handler for registering listeners.
func (s *Server) Events() *events.Handler {
	return s.events
}

// Resolver returns the DNS resolver.
func (s *Server) Resolver() *resolver.Resolver {
	return s.resolver
}

// IPPool returns the IP pool (may be nil if no ips.toml loaded).
func (s *Server) IPPool() *ippool.Pool {
	return s.ips
}

// DomainPool returns the domain rules pool (may be nil if no domains.toml loaded).
func (s *Server) DomainPool() *domainpool.Pool {
	return s.domains
}

// Config returns the server configuration.
func (s *Server) Config() *configloader.ServerConfig {
	return s.cfg
}
