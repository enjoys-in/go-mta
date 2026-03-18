package config

// Config holds all configuration needed by delivery adapters.
// Populate from environment variables, TOML, or any other source.
type Config struct {
	// Delivery method: "relay", "direct", "http"
	Method string

	// --- Relay settings ---
	RelayHost string
	RelayPort int
	RelayUser string
	RelayPass string
	RelayTLS  bool
	RelayAuth bool

	// --- Direct delivery settings ---
	DirectPort    int
	DirectHELO    string
	DirectTLS     bool
	DirectTimeout int // seconds

	// --- HTTP delivery settings ---
	HTTPURL     string
	HTTPMethod  string // POST, PUT
	HTTPHeaders map[string]string
	HTTPTimeout int // seconds

	// --- General ---
	// Extra key-value pairs for custom adapter needs
	Extra map[string]string
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Method:     "direct",
		RelayPort:  587,
		DirectPort: 25,
		DirectHELO: "localhost",
		HTTPMethod: "POST",
	}
}
