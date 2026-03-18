package http

import (
	"bytes"
	"fmt"
	"io"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/enjoys-in/go-mta/internal/mta/config"
	"github.com/enjoys-in/go-mta/internal/mta/delivery"
)

const adapterName = "http"

func init() {
	delivery.Register(adapterName, func() delivery.Adapter {
		return &HTTP{}
	})
}

// HTTP delivers email data via an HTTP endpoint (webhook / injection API).
// The sender and recipients are passed as HTTP headers so the receiving
// service knows the envelope information.
type HTTP struct{}

func (h *HTTP) Name() string { return adapterName }

func (h *HTTP) Deliver(from string, to []string, localIP string, cfg config.Config, data []byte) error {
	if cfg.HTTPURL == "" {
		return fmt.Errorf("http: HTTPURL is required in config")
	}

	method := cfg.HTTPMethod
	if method == "" {
		method = nethttp.MethodPost
	}

	timeout := time.Duration(cfg.HTTPTimeout) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	req, err := nethttp.NewRequest(method, cfg.HTTPURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("http: build request: %w", err)
	}

	req.Header.Set("Content-Type", "message/rfc822")
	req.Header.Set("X-Envelope-From", from)
	req.Header.Set("X-Envelope-To", strings.Join(to, ","))
	req.Header.Set("X-Local-IP", localIP)

	// Apply extra headers from config.
	for k, v := range cfg.HTTPHeaders {
		req.Header.Set(k, v)
	}

	client := &nethttp.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("http: server returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
