package relay

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/enjoys-in/go-mta/internal/mta/config"
	"github.com/enjoys-in/go-mta/internal/mta/delivery"
)

const adapterName = "relay"

func init() {
	delivery.Register(adapterName, func() delivery.Adapter {
		return &Relay{}
	})
}

// Relay delivers email through an upstream SMTP relay using emersion/go-smtp.
type Relay struct{}

func (r *Relay) Name() string { return adapterName }

func (r *Relay) Deliver(ctx context.Context, from string, to []string, localIP string, cfg config.Config, data []byte) error {
	addr := net.JoinHostPort(cfg.RelayHost, strconv.Itoa(cfg.RelayPort))

	// Build a custom dialer bound to the specified local IP.
	localAddr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(localIP, "0"))
	if err != nil {
		return fmt.Errorf("relay: resolve local IP: %w", err)
	}

	timeout := time.Duration(cfg.RelayTimeout) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	dialer := &net.Dialer{LocalAddr: localAddr, Timeout: timeout}

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("relay: dial %s: %w", addr, err)
	}

	tlsCfg, err := buildTLSConfig(cfg.RelayHost, cfg.TLSSkipVerify, cfg.TLSCAFile)
	if err != nil {
		conn.Close()
		return fmt.Errorf("relay: tls config: %w", err)
	}

	var c *smtp.Client
	if cfg.RelayTLS {
		// Implicit TLS (port 465): wrap connection in TLS first.
		tlsConn := tls.Client(conn, tlsCfg)
		c = smtp.NewClient(tlsConn)
	} else {
		// Plain connection, attempt STARTTLS upgrade.
		c, err = smtp.NewClientStartTLS(conn, tlsCfg)
		if err != nil {
			conn.Close()
			return fmt.Errorf("relay: smtp client starttls: %w", err)
		}
	}
	defer c.Close()

	// Authenticate only when explicitly enabled via RelayAuth.
	if cfg.RelayAuth && cfg.RelayUser != "" {
		auth := sasl.NewPlainClient("", cfg.RelayUser, cfg.RelayPass)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("relay: auth: %w", err)
		}
	}

	// Send the message.
	if err := c.Mail(from, nil); err != nil {
		return fmt.Errorf("relay: MAIL FROM: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt, nil); err != nil {
			return fmt.Errorf("relay: RCPT TO <%s>: %w", rcpt, err)
		}
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("relay: DATA: %w", err)
	}
	if _, err := bytes.NewReader(data).WriteTo(w); err != nil {
		w.Close()
		return fmt.Errorf("relay: write data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("relay: close data: %w", err)
	}

	return c.Quit()
}

// buildTLSConfig creates a *tls.Config honouring skip-verify and custom CA.
func buildTLSConfig(serverName string, skipVerify bool, caFile string) (*tls.Config, error) {
	cfg := &tls.Config{
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
		cfg.RootCAs = pool
	}
	return cfg, nil
}
