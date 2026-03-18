package delivery

import (
	"github.com/enjoys-in/go-mta/internal/mta/config"
)

// Adapter is the common interface every delivery method must implement.
type Adapter interface {
	// Deliver sends raw email data.
	//   from    – envelope sender
	//   to      – envelope recipients
	//   localIP – local IP address to bind for outbound connections
	//   cfg     – full configuration (adapter picks what it needs)
	//   data    – raw RFC-5322 message bytes
	Deliver(from string, to []string, localIP string, cfg config.Config, data []byte) error

	// Name returns the adapter identifier (e.g. "relay", "direct", "http").
	Name() string
}
