// Package mta provides a pluggable email delivery library.
//
// Import the adapters you need (blank imports register them via init()):
//
//	import (
//	    "github.com/enjoys-in/go-mta"
//	    _ "github.com/enjoys-in/go-mta/delivery/relay"
//	    _ "github.com/enjoys-in/go-mta/delivery/direct"
//	    _ "github.com/enjoys-in/go-mta/delivery/http"
//	)
//
// Then call mta.Send():
//
//	cfg := config.Config{Method: "direct", ...}
//	err := mta.Send(ctx, "sender@example.com", []string{"rcpt@example.com"}, "1.2.3.4", cfg, rawMsg)
package mta

import (
	"context"
	"fmt"

	"github.com/enjoys-in/go-mta/internal/mta/config"
	"github.com/enjoys-in/go-mta/internal/mta/delivery"
)

// Send delivers an email using the adapter specified by cfg.Method.
//
//	ctx     – context for cancellation and deadlines
//	from    – envelope sender address
//	to      – envelope recipient addresses
//	localIP – local IP address to bind for outbound connections
//	cfg     – delivery configuration (method, credentials, etc.)
//	data    – raw RFC-5322 message bytes
func Send(ctx context.Context, from string, to []string, localIP string, cfg config.Config, data []byte) error {
	if cfg.Method == "" {
		return fmt.Errorf("mta: config.Method is required")
	}
	if localIP == "" {
		return fmt.Errorf("mta: localIP is required")
	}

	adapter, err := delivery.New(cfg.Method)
	if err != nil {
		return err
	}
	return adapter.Deliver(ctx, from, to, localIP, cfg, data)
}

// SendWith delivers using a specific, pre-created adapter.
func SendWith(ctx context.Context, adapter delivery.Adapter, from string, to []string, localIP string, cfg config.Config, data []byte) error {
	if localIP == "" {
		return fmt.Errorf("mta: localIP is required")
	}
	return adapter.Deliver(ctx, from, to, localIP, cfg, data)
}

// Available returns the names of all registered delivery adapters.
func Available() []string {
	return delivery.Available()
}
