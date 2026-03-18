package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	mtacore "github.com/enjoys-in/go-mta/internal/mta"
	mtacfg "github.com/enjoys-in/go-mta/internal/mta/config"

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

	// Resolver (IP lookup strategy)
	strategy := resolver.ParseStrategy(cfg.IPLookupStrategy)
	res := resolver.New(strategy, 0)

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

	// Queue — worker func is the delivery pipeline.
	s.queue = queue.New(cfg.QueueSize, cfg.QueueWorkers, s.processJob)

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
	s.log.Info("server started",
		slog.String("method", s.cfg.Method),
		slog.Int("workers", s.cfg.QueueWorkers),
		slog.String("ip_lookup", string(s.resolver.Strategy())),
	)
}

// Submit creates a delivery job and enqueues it.
// Returns the generated job ID or an error if preflight fails.
func (s *Server) Submit(from string, to []string, data []byte) (string, error) {
	msg := types.AcquireMessage()
	msg.ID = generateID()
	msg.From = from
	msg.To = append(msg.To, to...)
	msg.Data = append(msg.Data, data...)
	msg.Size = len(data)
	msg.LocalIP = s.rotator.Next()
	msg.CreatedAt = time.Now()

	// Preflight
	if err := s.preflight.Check(msg); err != nil {
		s.events.Emit(events.Event{
			Type:      types.EventPreflightFail,
			Timestamp: time.Now(),
			JobID:     msg.ID,
			From:      from,
			Method:    s.cfg.Method,
			Err:       err,
		})
		types.ReleaseMessage(msg)
		return "", err
	}

	job := types.AcquireJob()
	job.Message = msg
	job.Method = s.cfg.Method
	job.MaxRetries = s.cfg.MaxRetries
	job.CreatedAt = time.Now()

	s.events.Emit(events.Event{
		Type:      types.EventQueued,
		Timestamp: time.Now(),
		JobID:     msg.ID,
		From:      from,
		LocalIP:   msg.LocalIP,
		Method:    s.cfg.Method,
	})

	if !s.queue.Enqueue(job) {
		types.ReleaseJob(job)
		return "", fmt.Errorf("server: queue full")
	}
	return msg.ID, nil
}

// Shutdown gracefully stops the server, draining the queue and events.
func (s *Server) Shutdown(ctx context.Context) {
	s.events.Emit(events.Event{
		Type:      types.EventShutdownStart,
		Timestamp: time.Now(),
	})
	s.log.Info("shutting down...")

	// Wait for any background init (cache warming) to finish.
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

// processJob is the queue worker function — the full delivery pipeline.
// Delivers to each recipient domain concurrently via goroutines.
func (s *Server) processJob(ctx context.Context, job *types.Job) error {
	defer types.ReleaseJob(job)
	msg := job.Message

	// --- Emit MailFrom event (non-blocking) ---
	s.events.Emit(events.Event{
		Type: types.EventMailFrom, Timestamp: time.Now(),
		JobID: msg.ID, From: msg.From, LocalIP: msg.LocalIP, Method: job.Method,
	})

	// --- Emit RcptTo events (non-blocking, one per recipient) ---
	for _, rcpt := range msg.To {
		s.events.Emit(events.Event{
			Type: types.EventRcptTo, Timestamp: time.Now(),
			JobID: msg.ID, From: msg.From, To: rcpt, LocalIP: msg.LocalIP, Method: job.Method,
		})
	}

	// Group recipients by domain for parallel delivery.
	domainRcpts := make(map[string][]string)
	for _, rcpt := range msg.To {
		parts := strings.SplitN(rcpt, "@", 2)
		if len(parts) == 2 {
			domainRcpts[parts[1]] = append(domainRcpts[parts[1]], rcpt)
		}
	}

	// --- Rate limit check (async per-domain, non-blocking) ---
	type rlResult struct {
		domain  string
		allowed bool
	}
	rlCh := make(chan rlResult, len(domainRcpts))
	for domain := range domainRcpts {
		domain := domain
		go func() {
			allowed, err := s.limiter.Allow(ctx, domain)
			if err != nil {
				s.log.Error("rate limit check failed", err,
					slog.String("job_id", msg.ID), slog.String("domain", domain))
			}
			rlCh <- rlResult{domain: domain, allowed: allowed || err != nil}
		}()
	}

	// Collect rate limit results, filter out blocked domains.
	allowedDomains := make(map[string]bool, len(domainRcpts))
	for range domainRcpts {
		r := <-rlCh
		if !r.allowed {
			s.events.Emit(events.Event{
				Type: types.EventRateLimited, Timestamp: time.Now(),
				JobID: msg.ID, From: msg.From, LocalIP: msg.LocalIP, Method: job.Method,
				Meta: map[string]string{"domain": r.domain},
			})
		}
		allowedDomains[r.domain] = r.allowed
	}

	// --- Emit DataStart ---
	s.events.Emit(events.Event{
		Type: types.EventDataStart, Timestamp: time.Now(),
		JobID: msg.ID, From: msg.From, LocalIP: msg.LocalIP, Method: job.Method,
	})

	// --- Deliver per-domain concurrently ---
	type deliveryResult struct {
		domain   string
		localIP  string
		err      error
		attempts int
	}
	resCh := make(chan deliveryResult, len(domainRcpts))

	var dwg sync.WaitGroup
	for domain, rcpts := range domainRcpts {
		if !allowedDomains[domain] {
			resCh <- deliveryResult{domain: domain, localIP: msg.LocalIP, err: fmt.Errorf("rate limited for domain %s", domain)}
			continue
		}

		dwg.Add(1)
		go func(domain string, rcpts []string) {
			defer dwg.Done()

			// Resolve MX for this domain and pick a family-compatible source IP.
			localIP := msg.LocalIP
			mxHosts, _ := s.resolver.LookupMX(ctx, domain)
			if len(mxHosts) > 0 {
				localIP = s.pickSourceIP(ctx, mxHosts[0].Host)
			}

			domainCfg := s.buildMTAConfig(localIP)

			s.events.Emit(events.Event{
				Type: types.EventDeliveryAttempt, Timestamp: time.Now(),
				JobID: msg.ID, From: msg.From, LocalIP: localIP, Method: job.Method, Attempt: 1,
			})

			attempts, err := s.retrier.Do(ctx, msg.ID, func() error {
				return mtacore.Send(msg.From, rcpts, localIP, domainCfg, msg.Data)
			})

			resCh <- deliveryResult{domain: domain, localIP: localIP, err: err, attempts: attempts}
		}(domain, rcpts)
	}

	// Wait for all goroutines, then close results channel.
	go func() {
		dwg.Wait()
		close(resCh)
	}()

	// --- Collect results ---
	var errs []string
	for dr := range resCh {
		if dr.err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", dr.domain, dr.err))

			// Bounce detection (async, non-blocking).
			if info := s.bouncer.Classify(dr.err); info != nil {
				s.events.Emit(events.Event{
					Type: types.EventBounceDetected, Timestamp: time.Now(),
					JobID: msg.ID, From: msg.From, LocalIP: dr.localIP, Method: job.Method,
					Err: dr.err,
					Meta: map[string]string{
						"bounce_category": info.Category,
						"smtp_code":       fmt.Sprintf("%d", info.Code),
						"domain":          dr.domain,
					},
				})
			}

			if dr.attempts >= job.MaxRetries {
				s.events.Emit(events.Event{
					Type: types.EventRetryExhausted, Timestamp: time.Now(),
					JobID: msg.ID, From: msg.From, LocalIP: dr.localIP, Method: job.Method,
					Attempt: dr.attempts, Err: dr.err,
				})
			}

			s.events.Emit(events.Event{
				Type: types.EventDeliveryFailed, Timestamp: time.Now(),
				JobID: msg.ID, From: msg.From, LocalIP: dr.localIP, Method: job.Method,
				Attempt: dr.attempts, Err: dr.err,
				Meta: map[string]string{"domain": dr.domain},
			})
		} else {
			s.events.Emit(events.Event{
				Type: types.EventDeliverySuccess, Timestamp: time.Now(),
				JobID: msg.ID, From: msg.From, LocalIP: dr.localIP, Method: job.Method,
				Attempt: dr.attempts, Meta: map[string]string{"domain": dr.domain},
			})
		}
	}

	// --- Emit DataEnd ---
	s.events.Emit(events.Event{
		Type: types.EventDataEnd, Timestamp: time.Now(),
		JobID: msg.ID, From: msg.From, LocalIP: msg.LocalIP, Method: job.Method,
	})

	if len(errs) > 0 {
		err := fmt.Errorf("delivery errors: %s", strings.Join(errs, "; "))
		s.log.Error("partial delivery failure", err,
			slog.String("job_id", msg.ID),
			slog.Int("failed_domains", len(errs)),
			slog.Int("total_domains", len(domainRcpts)),
		)
		return err
	}

	s.log.Info("delivered",
		slog.String("job_id", msg.ID),
		slog.String("from", msg.From),
		slog.Int("rcpt_count", len(msg.To)),
		slog.Int("domains", len(domainRcpts)),
	)
	return nil
}

// pickSourceIP resolves MX host IPs, classifies their address families,
// and selects a source IP from the rotation pool that matches.
//
// Rules:
//   - MX has both IPv4 and IPv6 → use weighted 2:1 IPv6 preference.
//   - MX has only IPv4 → pick an IPv4 source IP.
//   - MX has only IPv6 → pick an IPv6 source IP.
//   - If the matching family is empty in our pool, fall back to any IP.
func (s *Server) pickSourceIP(ctx context.Context, mxHost string) string {
	mxIPs, err := s.resolver.ResolveHost(ctx, mxHost)
	if err != nil || len(mxIPs) == 0 {
		// Can't resolve — use the default weighted preference.
		return s.rotator.NextPreferIPv6()
	}

	v4, v6 := resolver.ClassifyIPs(mxIPs)
	hasV4 := len(v4) > 0
	hasV6 := len(v6) > 0

	switch {
	case hasV4 && hasV6:
		// MX supports both — use 2:1 IPv6 weighted selection.
		return s.rotator.NextPreferIPv6()
	case hasV6:
		// MX only has IPv6 — pick an IPv6 source.
		return s.rotator.NextForFamily(rotation.FamilyIPv6)
	default:
		// MX only has IPv4 — pick an IPv4 source.
		return s.rotator.NextForFamily(rotation.FamilyIPv4)
	}
}

// buildMTAConfig converts server config into the internal MTA config.
func (s *Server) buildMTAConfig(localIP string) mtacfg.Config {
	return mtacfg.Config{
		Method:        s.cfg.Method,
		RelayHost:     s.cfg.RelayHost,
		RelayPort:     s.cfg.RelayPort,
		RelayUser:     s.cfg.RelayUser,
		RelayPass:     s.cfg.RelayPass,
		RelayTLS:      s.cfg.RelayTLS,
		DirectPort:    s.cfg.DirectPort,
		DirectHELO:    s.cfg.DirectHELO,
		DirectTLS:     s.cfg.DirectTLS,
		DirectTimeout: s.cfg.DirectTimeout,
		HTTPURL:       s.cfg.HTTPURL,
		HTTPMethod:    s.cfg.HTTPMethod,
		HTTPHeaders:   s.cfg.HTTPHeaders,
		HTTPTimeout:   s.cfg.HTTPTimeout,
	}
}

func generateID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}
