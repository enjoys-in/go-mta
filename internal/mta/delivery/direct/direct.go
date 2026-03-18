package direct

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/enjoys-in/go-mta/internal/mta/config"
	"github.com/enjoys-in/go-mta/internal/mta/delivery"
)

const adapterName = "direct"

func init() {
	delivery.Register(adapterName, func() delivery.Adapter {
		return &Direct{}
	})
}

// Direct delivers email straight to MX servers using net/smtp.
// It is a pure delivery pipe — MX resolution, domain grouping, chunking,
// rate limiting, and throttling are all handled by the pipeline layer.
// The adapter receives pre-resolved MX hosts via cfg.MXHosts.
type Direct struct{}

func (d *Direct) Name() string { return adapterName }

// RcptError records a per-recipient RCPT TO failure.
type RcptError struct {
	Rcpt string
	Err  error
}

func (e *RcptError) Error() string { return fmt.Sprintf("RCPT TO <%s>: %v", e.Rcpt, e.Err) }
func (e *RcptError) Unwrap() error { return e.Err }

// MultiRcptError aggregates per-recipient failures while some recipients succeeded.
type MultiRcptError struct {
	Succeeded []string
	Failed    []RcptError
}

func (e *MultiRcptError) Error() string {
	parts := make([]string, len(e.Failed))
	for i, f := range e.Failed {
		parts[i] = f.Error()
	}
	return fmt.Sprintf("direct: %d/%d recipients failed: %s",
		len(e.Failed), len(e.Succeeded)+len(e.Failed), strings.Join(parts, "; "))
}

// Deliver sends an email over a single SMTP connection.
// All recipients MUST belong to the same domain. The pipeline layer is
// responsible for grouping by domain, chunking by max-per-connection,
// resolving MX records, and throttling between batches.
//
// cfg.MXHosts must contain pre-resolved MX hostnames for the target domain.
// If empty, the adapter falls back to the first recipient's domain as A-record.
func (d *Direct) Deliver(ctx context.Context, from string, to []string, localIP string, cfg config.Config, data []byte) error {
	if len(to) == 0 {
		return fmt.Errorf("direct: no recipients")
	}

	timeout := time.Duration(cfg.DirectTimeout) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	helo := cfg.DirectHELO
	if helo == "" {
		helo = "localhost"
	}

	port := cfg.DirectPort
	if port == 0 {
		port = 25
	}

	localAddr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(localIP, "0"))
	if err != nil {
		return fmt.Errorf("direct: resolve local IP: %w", err)
	}

	// Use pre-resolved MX hosts from pipeline; fallback to recipient domain.
	mxHosts := cfg.MXHosts
	if len(mxHosts) == 0 {
		parts := strings.SplitN(to[0], "@", 2)
		if len(parts) == 2 {
			mxHosts = []string{parts[1]}
		}
	}

	var lastErr error
	for _, host := range mxHosts {
		host = strings.TrimSuffix(host, ".")
		addr := net.JoinHostPort(host, strconv.Itoa(port))

		succeeded, rcptFails, err := d.tryHost(ctx, from, to, addr, host, localAddr, helo, timeout, cfg, data)
		if err != nil {
			lastErr = err
			continue
		}

		if len(rcptFails) > 0 {
			return &MultiRcptError{Succeeded: succeeded, Failed: rcptFails}
		}
		return nil
	}

	return fmt.Errorf("direct: all MX hosts failed: %w", lastErr)
}

func (d *Direct) tryHost(ctx context.Context, from string, rcpts []string, addr, host string, localAddr *net.TCPAddr, helo string, timeout time.Duration, cfg config.Config, data []byte) (succeeded []string, rcptFails []RcptError, err error) {
	dialer := &net.Dialer{LocalAddr: localAddr, Timeout: timeout}

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("smtp client %s: %w", host, err)
	}

	if err := c.Hello(helo); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("HELO %s: %w", host, err)
	}

	if cfg.DirectTLS {
		tlsCfg, tlsErr := buildTLSConfig(host, cfg.TLSSkipVerify, cfg.TLSCAFile)
		if tlsErr != nil {
			c.Close()
			return nil, nil, fmt.Errorf("tls config %s: %w", host, tlsErr)
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			// Non-fatal: server may not support STARTTLS.
			_ = err
		}
	}

	if err := c.Mail(from); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("MAIL FROM %s: %w", host, err)
	}

	// Track per-recipient RCPT results.
	var accepted []string
	for _, rcpt := range rcpts {
		if err := c.Rcpt(rcpt); err != nil {
			rcptFails = append(rcptFails, RcptError{Rcpt: rcpt, Err: err})
		} else {
			accepted = append(accepted, rcpt)
		}
	}

	if len(accepted) == 0 {
		c.Close()
		return nil, rcptFails, fmt.Errorf("all recipients rejected by %s", host)
	}

	w, err := c.Data()
	if err != nil {
		c.Close()
		return nil, rcptFails, fmt.Errorf("DATA %s: %w", host, err)
	}

	if _, err := w.Write(data); err != nil {
		w.Close()
		c.Close()
		return nil, rcptFails, fmt.Errorf("write data %s: %w", host, err)
	}

	if err := w.Close(); err != nil {
		c.Close()
		return nil, rcptFails, fmt.Errorf("close data %s: %w", host, err)
	}

	c.Quit()
	return accepted, rcptFails, nil
}

// buildTLSConfig creates a *tls.Config honouring skip-verify and custom CA.
func buildTLSConfig(serverName string, skipVerify bool, caFile string) (*tls.Config, error) {
	tlsCfg := &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: skipVerify,
	}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("invalid CA certificate in %s", caFile)
		}
		tlsCfg.RootCAs = pool
	}
	return tlsCfg, nil
}
