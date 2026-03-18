package preflight

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/enjoys-in/go-mta/pkg/logger"
	"github.com/enjoys-in/go-mta/pkg/types"
)

// Checker validates messages before they enter the delivery pipeline.
type Checker struct {
	maxSize       int
	maxRecipients int
	log           *logger.Logger
}

// New creates a preflight checker.
func New(maxSize, maxRecipients int) *Checker {
	if maxSize <= 0 {
		maxSize = types.DefaultMaxMessageSize
	}
	if maxRecipients <= 0 {
		maxRecipients = types.DefaultMaxRecipients
	}
	return &Checker{
		maxSize:       maxSize,
		maxRecipients: maxRecipients,
		log:           logger.New(types.ComponentPreflight),
	}
}

// Check runs all preflight validations on a message.
func (c *Checker) Check(msg *types.Message) error {
	if msg.From == "" {
		return fmt.Errorf("preflight: empty sender")
	}
	if len(msg.To) == 0 {
		return fmt.Errorf("preflight: no recipients")
	}
	if len(msg.To) > c.maxRecipients {
		c.log.Warn("too many recipients",
			slog.Int("count", len(msg.To)),
			slog.Int("max", c.maxRecipients),
		)
		return fmt.Errorf("preflight: recipient count %d exceeds limit %d", len(msg.To), c.maxRecipients)
	}
	if len(msg.Data) == 0 {
		return fmt.Errorf("preflight: empty message body")
	}
	if len(msg.Data) > c.maxSize {
		c.log.Warn("message too large",
			slog.Int("size", len(msg.Data)),
			slog.Int("max", c.maxSize),
		)
		return fmt.Errorf("preflight: message size %d exceeds limit %d", len(msg.Data), c.maxSize)
	}
	if msg.LocalIP == "" {
		return fmt.Errorf("preflight: localIP is required")
	}

	// Validate recipient format.
	for _, rcpt := range msg.To {
		if !strings.Contains(rcpt, "@") || strings.HasPrefix(rcpt, "@") || strings.HasSuffix(rcpt, "@") {
			return fmt.Errorf("preflight: invalid recipient %q", rcpt)
		}
	}

	return nil
}
