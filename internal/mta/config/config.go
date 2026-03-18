package config

// Config holds all configuration needed by delivery adapters.
// Populate from environment variables, TOML, or any other source.
type Config struct {
	// Delivery method: "relay", "direct", "http"
	Method string

	// --- Relay settings ---
	RelayHost    string
	RelayPort    int
	RelayUser    string
	RelayPass    string
	RelayTLS     bool
	RelayAuth    bool
	RelayTimeout int // seconds (0 = 30s default)

	// --- Direct delivery settings ---
	DirectPort    int
	DirectHELO    string
	DirectTLS     bool
	DirectTimeout int      // seconds
	MXHosts       []string // pre-resolved MX hostnames (pipeline provides these)

	// --- HTTP delivery settings ---
	HTTPURL        string
	HTTPMethod     string // POST, PUT
	HTTPHeaders    map[string]string
	HTTPTimeout    int    // seconds
	HTTPAuthType   string // "bearer", "hmac-sha256", "" (none)
	HTTPAuthSecret string // token for bearer, secret key for HMAC

	// --- TLS ---
	TLSSkipVerify bool   // skip certificate verification
	TLSCAFile     string // path to custom CA certificate file

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
