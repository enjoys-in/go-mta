package server

import (
	"context"

	mtacfg "github.com/enjoys-in/go-mta/internal/mta/config"
	"github.com/enjoys-in/go-mta/pkg/resolver"
	"github.com/enjoys-in/go-mta/pkg/rotation"
)

// pickSourceIP resolves MX host IPs, classifies their address families,
// and selects a source IP from the rotation pool that matches.
//
// Rules:
//   - MX has both IPv4 and IPv6 → use weighted 2:1 IPv6 preference.
//   - MX has only IPv4 → pick an IPv4 source IP.
//   - MX has only IPv6 → pick an IPv6 source IP.
//   - If the matching family is empty in our pool, fall back to any IP.
func (s *Server) pickSourceIP(ctx context.Context, mxHost string) string {
	mxIPs, err := s.resolver.ResolveHost(ctx, mxHost)
	if err != nil || len(mxIPs) == 0 {
		return s.rotator.NextPreferIPv6()
	}

	v4, v6 := resolver.ClassifyIPs(mxIPs)
	hasV4 := len(v4) > 0
	hasV6 := len(v6) > 0

	switch {
	case hasV4 && hasV6:
		return s.rotator.NextPreferIPv6()
	case hasV6:
		return s.rotator.NextForFamily(rotation.FamilyIPv6)
	default:
		return s.rotator.NextForFamily(rotation.FamilyIPv4)
	}
}

// buildMTAConfig converts server config into the internal MTA config.
func (s *Server) buildMTAConfig(localIP string) mtacfg.Config {
	return mtacfg.Config{
		Method:         s.cfg.Method,
		RelayHost:      s.cfg.RelayHost,
		RelayPort:      s.cfg.RelayPort,
		RelayUser:      s.cfg.RelayUser,
		RelayPass:      s.cfg.RelayPass,
		RelayTLS:       s.cfg.RelayTLS,
		RelayAuth:      s.cfg.RelayAuth,
		RelayTimeout:   s.cfg.RelayTimeout,
		DirectPort:     s.cfg.DirectPort,
		DirectHELO:     s.cfg.DirectHELO,
		DirectTLS:      s.cfg.DirectTLS,
		DirectTimeout:  s.cfg.DirectTimeout,
		HTTPURL:        s.cfg.HTTPURL,
		HTTPMethod:     s.cfg.HTTPMethod,
		HTTPHeaders:    s.cfg.HTTPHeaders,
		HTTPTimeout:    s.cfg.HTTPTimeout,
		HTTPAuthType:   s.cfg.HTTPAuthType,
		HTTPAuthSecret: s.cfg.HTTPAuthSecret,
		TLSSkipVerify:  s.cfg.TLSInsecureSkipVerify,
		TLSCAFile:      s.cfg.TLSCACertFile,
	}
}
