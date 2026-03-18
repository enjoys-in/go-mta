package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	mtacore "github.com/enjoys-in/go-mta/internal/mta"

	"github.com/enjoys-in/go-mta/pkg/events"
	"github.com/enjoys-in/go-mta/pkg/types"
)

// deliveryResult holds the outcome of a single per-domain delivery attempt.
type deliveryResult struct {
	domain   string
	localIP  string
	method   string
	err      error
	attempts int
}

// processJob is the queue worker function — the full delivery pipeline.
// Delivers to each recipient domain concurrently via goroutines.
func (s *Server) processJob(ctx context.Context, job *types.Job) error {
	defer types.ReleaseJob(job)
	msg := job.Message

	s.emitEnvelopeEvents(msg, job.Method)

	domainRcpts := groupByDomain(msg.To)
	allowedDomains := s.checkRateLimits(ctx, msg, job.Method, domainRcpts)

	s.events.Emit(events.Event{
		Type: types.EventDataStart, Timestamp: time.Now(),
		JobID: msg.ID, From: msg.From, LocalIP: msg.LocalIP, Method: job.Method,
	})

	resCh := s.deliverDomains(ctx, msg, job, domainRcpts, allowedDomains)

	var errs []string
	for dr := range resCh {
		s.handleDeliveryResult(msg, job, dr, &errs)
	}

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

func (s *Server) emitEnvelopeEvents(msg *types.Message, method string) {
	s.events.Emit(events.Event{
		Type: types.EventMailFrom, Timestamp: time.Now(),
		JobID: msg.ID, From: msg.From, LocalIP: msg.LocalIP, Method: method,
	})
	for _, rcpt := range msg.To {
		s.events.Emit(events.Event{
			Type: types.EventRcptTo, Timestamp: time.Now(),
			JobID: msg.ID, From: msg.From, To: rcpt, LocalIP: msg.LocalIP, Method: method,
		})
	}
}

func groupByDomain(recipients []string) map[string][]string {
	m := make(map[string][]string)
	for _, rcpt := range recipients {
		parts := strings.SplitN(rcpt, "@", 2)
		if len(parts) == 2 {
			m[parts[1]] = append(m[parts[1]], rcpt)
		}
	}
	return m
}

func (s *Server) checkRateLimits(ctx context.Context, msg *types.Message, method string, domainRcpts map[string][]string) map[string]bool {
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

	allowed := make(map[string]bool, len(domainRcpts))
	for range domainRcpts {
		r := <-rlCh
		if !r.allowed {
			s.events.Emit(events.Event{
				Type: types.EventRateLimited, Timestamp: time.Now(),
				JobID: msg.ID, From: msg.From, LocalIP: msg.LocalIP, Method: method,
				Meta: map[string]string{"domain": r.domain},
			})
		}
		allowed[r.domain] = r.allowed
	}
	return allowed
}

func (s *Server) deliverDomains(ctx context.Context, msg *types.Message, job *types.Job, domainRcpts map[string][]string, allowed map[string]bool) <-chan deliveryResult {
	resCh := make(chan deliveryResult, len(domainRcpts))
	var wg sync.WaitGroup

	for domain, rcpts := range domainRcpts {
		if !allowed[domain] {
			resCh <- deliveryResult{domain: domain, localIP: msg.LocalIP, method: job.Method, err: fmt.Errorf("rate limited for domain %s", domain)}
			continue
		}

		wg.Add(1)
		go func(domain string, rcpts []string) {
			defer wg.Done()

			// Resolve MX hosts (cached via Dragonfly).
			localIP := msg.LocalIP
			mxHosts, _ := s.resolver.LookupMX(ctx, domain)
			var mxHostnames []string
			if len(mxHosts) > 0 {
				localIP = s.pickSourceIP(ctx, mxHosts[0].Host)
				for _, mx := range mxHosts {
					mxHostnames = append(mxHostnames, mx.Host)
				}
			}

			domainCfg := s.buildMTAConfig(localIP)
			domainCfg.MXHosts = mxHostnames

			method := job.Method
			maxPerConn := 5 // default
			if s.domains != nil {
				if rule := s.domains.Match(domain); rule != nil {
					if rule.DeliveryMethod != "" {
						method = rule.DeliveryMethod
						domainCfg.Method = method
					}
					if rule.MaxDeliveriesPerConnection > 0 {
						maxPerConn = rule.MaxDeliveriesPerConnection
					}
				}
			}

			// Chunk recipients by maxPerConn and deliver with throttle between rounds.
			chunks := chunkSlice(rcpts, maxPerConn)

			var lastErr error
			var totalAttempts int
			for i, chunk := range chunks {
				if i > 0 {
					select {
					case <-ctx.Done():
						resCh <- deliveryResult{domain: domain, localIP: localIP, method: method, err: ctx.Err(), attempts: totalAttempts}
						return
					case <-time.After(1 * time.Second):
					}
				}

				s.events.Emit(events.Event{
					Type: types.EventDeliveryAttempt, Timestamp: time.Now(),
					JobID: msg.ID, From: msg.From, LocalIP: localIP, Method: method, Attempt: i + 1,
				})

				attempts, err := s.retrier.Do(ctx, msg.ID, func() error {
					return mtacore.Send(ctx, msg.From, chunk, localIP, domainCfg, msg.Data)
				})
				totalAttempts += attempts

				// IP family fallback: if bind failed and fallback is enabled,
				// resolve MX IPs compatible with opposite family and retry unbound.
				if err != nil && s.cfg.IPFamilyFallback && isBindError(err) && len(mxHostnames) > 0 {
					s.log.Warn("bind failed, trying IP family fallback",
						slog.String("job_id", msg.ID),
						slog.String("domain", domain),
						slog.String("original_ip", localIP),
						slog.String("error", err.Error()),
					)
					fallbackIPs, unbind, resolveErr := s.resolver.ResolveCompatible(ctx, mxHostnames[0], localIP)
					if resolveErr == nil && len(fallbackIPs) > 0 {
						fbIP := localIP
						if unbind {
							fbIP = "" // let OS pick
						}
						fbCfg := s.buildMTAConfig(fbIP)
						fbCfg.MXHosts = mxHostnames
						fbCfg.Method = method

						fbAttempts, fbErr := s.retrier.Do(ctx, msg.ID, func() error {
							return mtacore.Send(ctx, msg.From, chunk, fbIP, fbCfg, msg.Data)
						})
						totalAttempts += fbAttempts
						if fbErr == nil {
							err = nil
						} else {
							err = fbErr
						}
					}
				}

				if err != nil {
					lastErr = err
				}
			}

			resCh <- deliveryResult{domain: domain, localIP: localIP, method: method, err: lastErr, attempts: totalAttempts}
		}(domain, rcpts)
	}

	go func() {
		wg.Wait()
		close(resCh)
	}()

	return resCh
}

// chunkSlice splits a slice into chunks of at most size n.
func chunkSlice(s []string, n int) [][]string {
	if n <= 0 {
		n = len(s)
	}
	var chunks [][]string
	for i := 0; i < len(s); i += n {
		end := i + n
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[i:end])
	}
	return chunks
}

func (s *Server) handleDeliveryResult(msg *types.Message, job *types.Job, dr deliveryResult, errs *[]string) {
	if dr.err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: %v", dr.domain, dr.err))

		if info := s.bouncer.Classify(dr.err); info != nil {
			s.events.Emit(events.Event{
				Type: types.EventBounceDetected, Timestamp: time.Now(),
				JobID: msg.ID, From: msg.From, LocalIP: dr.localIP, Method: dr.method,
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
				JobID: msg.ID, From: msg.From, LocalIP: dr.localIP, Method: dr.method,
				Attempt: dr.attempts, Err: dr.err,
			})
		}

		s.events.Emit(events.Event{
			Type: types.EventDeliveryFailed, Timestamp: time.Now(),
			JobID: msg.ID, From: msg.From, LocalIP: dr.localIP, Method: dr.method,
			Attempt: dr.attempts, Err: dr.err,
			Meta: map[string]string{"domain": dr.domain},
		})
	} else {
		s.events.Emit(events.Event{
			Type: types.EventDeliverySuccess, Timestamp: time.Now(),
			JobID: msg.ID, From: msg.From, LocalIP: dr.localIP, Method: dr.method,
			Attempt: dr.attempts, Meta: map[string]string{"domain": dr.domain},
		})
	}
}

// isBindError returns true if the error is a "bind: cannot assign requested address"
// or similar, indicating the source IP can't reach the target address family.
func isBindError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "bind: cannot assign requested address") ||
		strings.Contains(msg, "bind: address not available") ||
		strings.Contains(msg, "connect: network is unreachable")
}
