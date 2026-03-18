package configloader

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// ServerConfig is the full application configuration loaded from TOML / env.
type ServerConfig struct {
	// Delivery
	Method   string   `toml:"method"`
	LocalIPs []string `toml:"local_ips"`

	// TOML folder includes
	Include     []string `toml:"include"`      // e.g. [".toml"] to load all .toml in the dir
	IPsFile     string   `toml:"ips_file"`     // path to ips.toml (auto-set by LoadDir)
	DomainsFile string   `toml:"domains_file"` // path to domains.toml (auto-set by LoadDir)

	// IP lookup strategy
	IPLookupStrategy string `toml:"ip_lookup_strategy"` // Ipv4Only, Ipv6Only, Ipv4AndIpv6, Ipv4ThenIpv6, Ipv6ThenIpv4

	// Relay
	RelayHost string `toml:"relay_host"`
	RelayPort int    `toml:"relay_port"`
	RelayUser string `toml:"relay_user"`
	RelayPass string `toml:"relay_pass"`
	RelayTLS  bool   `toml:"relay_tls"`

	// Direct
	DirectPort    int    `toml:"direct_port"`
	DirectHELO    string `toml:"direct_helo"`
	DirectTLS     bool   `toml:"direct_tls"`
	DirectTimeout int    `toml:"direct_timeout"`

	// HTTP
	HTTPURL     string            `toml:"http_url"`
	HTTPMethod  string            `toml:"http_method"`
	HTTPHeaders map[string]string `toml:"http_headers"`
	HTTPTimeout int               `toml:"http_timeout"`

	// TLS
	TLSInsecureSkipVerify bool   `toml:"tls_insecure_skip_verify"`
	TLSCACertFile         string `toml:"tls_ca_cert_file"`

	// Cache (Dragonfly / Redis)
	CacheAddr     string `toml:"cache_addr"`
	CacheUser     string `toml:"cache_user"` // optional Redis/Dragonfly username
	CachePassword string `toml:"cache_pass"`
	CacheDB       int    `toml:"cache_db"`

	// Queue
	QueueSize    int `toml:"queue_size"`
	QueueWorkers int `toml:"queue_workers"`

	// Rate Limit
	RateLimitPerSec int `toml:"rate_limit_per_sec"`

	// Retry
	MaxRetries int `toml:"max_retries"`

	// Preflight
	MaxMessageSize int `toml:"max_message_size"`
	MaxRecipients  int `toml:"max_recipients"`
}

// LoadFile reads a TOML config file, then overlays environment variables.
func LoadFile(path string) (*ServerConfig, error) {
	cfg := &ServerConfig{}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("configloader: %w", err)
	}
	cfg.applyEnv()
	cfg.applyDefaults()
	return cfg, nil
}

// LoadDir reads all TOML files from a directory.
// It expects config.example.toml (or any main config), ips.toml, and domains.toml.
// The main config is loaded first, then ips/domains paths are auto-discovered.
func LoadDir(dir string) (*ServerConfig, error) {
	// Find the main config file (first .toml that isn't ips.toml or domains.toml).
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("configloader: read dir %s: %w", dir, err)
	}

	var mainCfgPath string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		name := strings.ToLower(e.Name())
		if name == "ips.toml" || name == "domains.toml" {
			continue
		}
		mainCfgPath = filepath.Join(dir, e.Name())
		break
	}

	var cfg *ServerConfig
	if mainCfgPath != "" {
		cfg = &ServerConfig{}
		if _, err := toml.DecodeFile(mainCfgPath, cfg); err != nil {
			return nil, fmt.Errorf("configloader: decode %s: %w", mainCfgPath, err)
		}
	} else {
		cfg = &ServerConfig{}
	}

	// Auto-discover ips.toml and domains.toml.
	ipsPath := filepath.Join(dir, "ips.toml")
	if _, err := os.Stat(ipsPath); err == nil {
		cfg.IPsFile = ipsPath
	}

	domainsPath := filepath.Join(dir, "domains.toml")
	if _, err := os.Stat(domainsPath); err == nil {
		cfg.DomainsFile = domainsPath
	}

	cfg.applyEnv()
	cfg.applyDefaults()
	return cfg, nil
}

// LoadEnv loads configuration purely from environment variables.
func LoadEnv() *ServerConfig {
	cfg := &ServerConfig{}
	cfg.applyEnv()
	cfg.applyDefaults()
	return cfg
}

func (c *ServerConfig) applyEnv() {
	if v := os.Getenv("GOMTA_METHOD"); v != "" {
		c.Method = v
	}
	if v := os.Getenv("GOMTA_LOCAL_IPS"); v != "" {
		c.LocalIPs = strings.Split(v, ",")
	}

	// Relay
	envStr(&c.RelayHost, "GOMTA_RELAY_HOST")
	envInt(&c.RelayPort, "GOMTA_RELAY_PORT")
	envStr(&c.RelayUser, "GOMTA_RELAY_USER")
	envStr(&c.RelayPass, "GOMTA_RELAY_PASS")
	envBool(&c.RelayTLS, "GOMTA_RELAY_TLS")

	// Direct
	envInt(&c.DirectPort, "GOMTA_DIRECT_PORT")
	envStr(&c.DirectHELO, "GOMTA_DIRECT_HELO")
	envBool(&c.DirectTLS, "GOMTA_DIRECT_TLS")
	envInt(&c.DirectTimeout, "GOMTA_DIRECT_TIMEOUT")

	// HTTP
	envStr(&c.HTTPURL, "GOMTA_HTTP_URL")
	envStr(&c.HTTPMethod, "GOMTA_HTTP_METHOD")
	envInt(&c.HTTPTimeout, "GOMTA_HTTP_TIMEOUT")

	// TLS
	envBool(&c.TLSInsecureSkipVerify, "GOMTA_TLS_INSECURE_SKIP_VERIFY")
	envStr(&c.TLSCACertFile, "GOMTA_TLS_CA_CERT_FILE")

	// Cache
	envStr(&c.CacheAddr, "GOMTA_CACHE_ADDR")
	envStr(&c.CacheUser, "GOMTA_CACHE_USER")
	envStr(&c.CachePassword, "GOMTA_CACHE_PASS")
	envInt(&c.CacheDB, "GOMTA_CACHE_DB")

	// Queue
	envInt(&c.QueueSize, "GOMTA_QUEUE_SIZE")
	envInt(&c.QueueWorkers, "GOMTA_QUEUE_WORKERS")

	// Rate
	envInt(&c.RateLimitPerSec, "GOMTA_RATE_LIMIT")
	envInt(&c.MaxRetries, "GOMTA_MAX_RETRIES")
	envInt(&c.MaxMessageSize, "GOMTA_MAX_MESSAGE_SIZE")
	envInt(&c.MaxRecipients, "GOMTA_MAX_RECIPIENTS")

	// IP lookup strategy
	envStr(&c.IPLookupStrategy, "GOMTA_IP_LOOKUP_STRATEGY")

	// TOML paths
	envStr(&c.IPsFile, "GOMTA_IPS_FILE")
	envStr(&c.DomainsFile, "GOMTA_DOMAINS_FILE")
}

func (c *ServerConfig) applyDefaults() {
	if c.Method == "" {
		c.Method = "direct"
	}
	if c.RelayPort == 0 {
		c.RelayPort = 587
	}
	if c.DirectPort == 0 {
		c.DirectPort = 25
	}
	if c.DirectHELO == "" {
		c.DirectHELO = "localhost"
	}
	if c.HTTPMethod == "" {
		c.HTTPMethod = "POST"
	}
	if c.CacheAddr == "" {
		c.CacheAddr = "127.0.0.1:6379"
	}
	if c.QueueSize == 0 {
		c.QueueSize = 10000
	}
	if c.QueueWorkers == 0 {
		c.QueueWorkers = 4
	}
	if c.RateLimitPerSec == 0 {
		c.RateLimitPerSec = 10
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = 5
	}
	if c.MaxMessageSize == 0 {
		c.MaxMessageSize = 25 * 1024 * 1024
	}
	if c.MaxRecipients == 0 {
		c.MaxRecipients = 100
	}
	if c.IPLookupStrategy == "" {
		c.IPLookupStrategy = "Ipv4Only"
	}
}

func envStr(dst *string, key string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

func envInt(dst *int, key string) {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			*dst = i
		}
	}
}

func envBool(dst *bool, key string) {
	if v := os.Getenv(key); v != "" {
		*dst = v == "true" || v == "1" || v == "yes"
	}
}
