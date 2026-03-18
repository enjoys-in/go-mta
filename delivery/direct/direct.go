package direct

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/enjoys-in/go-mta/config"
	"github.com/enjoys-in/go-mta/delivery"
)

const adapterName = "direct"

func init() {
	delivery.Register(adapterName, func() delivery.Adapter {
		return &Direct{}
	})
}

// Direct delivers email straight to the recipients' MX servers using net/smtp.
type Direct struct{}

func (d *Direct) Name() string { return adapterName }

func (d *Direct) Deliver(from string, to []string, localIP string, cfg config.Config, data []byte) error {
	// Group recipients by domain for MX lookup.
	domainRcpts := make(map[string][]string)
	for _, rcpt := range to {
		parts := strings.SplitN(rcpt, "@", 2)
		if len(parts) != 2 {
			return fmt.Errorf("direct: invalid recipient %q", rcpt)
		}
		domain := parts[1]
		domainRcpts[domain] = append(domainRcpts[domain], rcpt)
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

	var errs []string
	for domain, rcpts := range domainRcpts {
		if err := d.deliverToDomain(from, rcpts, domain, localAddr, helo, port, timeout, cfg.DirectTLS, data); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", domain, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("direct: delivery errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (d *Direct) deliverToDomain(from string, rcpts []string, domain string, localAddr *net.TCPAddr, helo string, port int, timeout time.Duration, useTLS bool, data []byte) error {
	mxRecords, err := net.LookupMX(domain)
	if err != nil || len(mxRecords) == 0 {
		// Fall back to the domain itself as the MX host.
		mxRecords = []*net.MX{{Host: domain, Pref: 0}}
	}

	var lastErr error
	for _, mx := range mxRecords {
		host := strings.TrimSuffix(mx.Host, ".")
		addr := net.JoinHostPort(host, strconv.Itoa(port))

		dialer := &net.Dialer{
			LocalAddr: localAddr,
			Timeout:   timeout,
		}

		conn, err := dialer.Dial("tcp", addr)
		if err != nil {
			lastErr = fmt.Errorf("dial %s: %w", addr, err)
			continue
		}

		c, err := smtp.NewClient(conn, host)
		if err != nil {
			conn.Close()
			lastErr = fmt.Errorf("smtp client %s: %w", host, err)
			continue
		}

		if err := c.Hello(helo); err != nil {
			c.Close()
			lastErr = fmt.Errorf("HELO %s: %w", host, err)
			continue
		}

		if useTLS {
			if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
				// Non-fatal: server may not support STARTTLS.
				_ = err
			}
		}

		if err := c.Mail(from); err != nil {
			c.Close()
			lastErr = fmt.Errorf("MAIL FROM %s: %w", host, err)
			continue
		}

		for _, rcpt := range rcpts {
			if err := c.Rcpt(rcpt); err != nil {
				c.Close()
				lastErr = fmt.Errorf("RCPT TO <%s> via %s: %w", rcpt, host, err)
				continue
			}
		}

		w, err := c.Data()
		if err != nil {
			c.Close()
			lastErr = fmt.Errorf("DATA %s: %w", host, err)
			continue
		}

		if _, err := w.Write(data); err != nil {
			w.Close()
			c.Close()
			lastErr = fmt.Errorf("write data %s: %w", host, err)
			continue
		}

		if err := w.Close(); err != nil {
			c.Close()
			lastErr = fmt.Errorf("close data %s: %w", host, err)
			continue
		}

		c.Quit()
		return nil // success on this MX
	}

	return fmt.Errorf("all MX hosts failed for %s: %w", domain, lastErr)
}
