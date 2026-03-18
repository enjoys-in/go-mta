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

			localIP := msg.LocalIP
			mxHosts, _ := s.resolver.LookupMX(ctx, domain)
			if len(mxHosts) > 0 {
				localIP = s.pickSourceIP(ctx, mxHosts[0].Host)
			}

			domainCfg := s.buildMTAConfig(localIP)

			method := job.Method
			if s.domains != nil {
				if rule := s.domains.Match(domain); rule != nil && rule.DeliveryMethod != "" {
					method = rule.DeliveryMethod
					domainCfg.Method = method
				}
			}

			s.events.Emit(events.Event{
				Type: types.EventDeliveryAttempt, Timestamp: time.Now(),
				JobID: msg.ID, From: msg.From, LocalIP: localIP, Method: method, Attempt: 1,
			})

			attempts, err := s.retrier.Do(ctx, msg.ID, func() error {
				return mtacore.Send(msg.From, rcpts, localIP, domainCfg, msg.Data)
			})

			resCh <- deliveryResult{domain: domain, localIP: localIP, method: method, err: err, attempts: attempts}
		}(domain, rcpts)
	}

	go func() {
		wg.Wait()
		close(resCh)
	}()

	return resCh
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
