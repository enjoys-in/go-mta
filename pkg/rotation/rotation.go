package rotation

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/enjoys-in/go-mta/pkg/logger"
	"github.com/enjoys-in/go-mta/pkg/resolver"
	"github.com/enjoys-in/go-mta/pkg/types"
)

// Family represents an IP address family.
type Family byte

const (
	FamilyIPv4 Family = 4
	FamilyIPv6 Family = 6
)

// Rotator cycles through a pool of local IP addresses with
// family-aware weighted round-robin selection.
type Rotator struct {
	all  []string // combined pool for generic Next()
	ipv4 []string // IPv4-only pool
	ipv6 []string // IPv6-only pool

	idxAll  atomic.Uint64
	idxV4   atomic.Uint64
	idxV6   atomic.Uint64
	idxPref atomic.Uint64 // counter for weighted preference

	mu  sync.RWMutex
	log *logger.Logger
}

// New creates a round-robin IP rotator.
// IPs are classified into IPv4 and IPv6 sub-pools automatically.
func New(ips []string) (*Rotator, error) {
	if len(ips) == 0 {
		return nil, fmt.Errorf("rotation: at least one IP required")
	}
	r := &Rotator{
		log: logger.New(types.ComponentRotation),
	}
	r.classify(ips)
	return r, nil
}

func (r *Rotator) classify(ips []string) {
	r.all = ips
	r.ipv4 = r.ipv4[:0]
	r.ipv6 = r.ipv6[:0]
	for _, ip := range ips {
		if resolver.IsIPv4(ip) {
			r.ipv4 = append(r.ipv4, ip)
		} else {
			r.ipv6 = append(r.ipv6, ip)
		}
	}
}

// Next returns the next IP in round-robin order (all families).
func (r *Rotator) Next() string {
	r.mu.RLock()
	n := uint64(len(r.all))
	r.mu.RUnlock()

	idx := r.idxAll.Add(1) - 1
	return r.all[idx%n]
}

// NextForFamily returns the next IP matching the requested address family.
// Falls back to any available IP if the requested family has no IPs.
func (r *Rotator) NextForFamily(f Family) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	switch f {
	case FamilyIPv4:
		if len(r.ipv4) > 0 {
			idx := r.idxV4.Add(1) - 1
			return r.ipv4[idx%uint64(len(r.ipv4))]
		}
	case FamilyIPv6:
		if len(r.ipv6) > 0 {
			idx := r.idxV6.Add(1) - 1
			return r.ipv6[idx%uint64(len(r.ipv6))]
		}
	}
	// Fallback to any.
	idx := r.idxAll.Add(1) - 1
	return r.all[idx%uint64(len(r.all))]
}

// NextPreferIPv6 picks an IP with a weighted 2:1 preference for IPv6.
// Every 3 calls: 2 return IPv6 (if available), 1 returns IPv4.
// If one family is empty, the other is always returned.
func (r *Rotator) NextPreferIPv6() string {
	r.mu.RLock()
	hasV4 := len(r.ipv4) > 0
	hasV6 := len(r.ipv6) > 0
	r.mu.RUnlock()

	if hasV6 && !hasV4 {
		return r.NextForFamily(FamilyIPv6)
	}
	if hasV4 && !hasV6 {
		return r.NextForFamily(FamilyIPv4)
	}

	// Both families available — 2:1 ratio favouring IPv6.
	slot := r.idxPref.Add(1) - 1
	if slot%3 < 2 {
		return r.NextForFamily(FamilyIPv6)
	}
	return r.NextForFamily(FamilyIPv4)
}

// HasIPv4 reports whether the pool has at least one IPv4 address.
func (r *Rotator) HasIPv4() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.ipv4) > 0
}

// HasIPv6 reports whether the pool has at least one IPv6 address.
func (r *Rotator) HasIPv6() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.ipv6) > 0
}

// SetIPs replaces the IP pool at runtime.
func (r *Rotator) SetIPs(ips []string) error {
	if len(ips) == 0 {
		return fmt.Errorf("rotation: at least one IP required")
	}
	r.mu.Lock()
	r.classify(ips)
	r.mu.Unlock()
	return nil
}

// IPs returns a copy of the current IP pool.
func (r *Rotator) IPs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cp := make([]string, len(r.all))
	copy(cp, r.all)
	return cp
}

// IPv4IPs returns a copy of only the IPv4 addresses in the pool.
func (r *Rotator) IPv4IPs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cp := make([]string, len(r.ipv4))
	copy(cp, r.ipv4)
	return cp
}

// IPv6IPs returns a copy of only the IPv6 addresses in the pool.
func (r *Rotator) IPv6IPs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cp := make([]string, len(r.ipv6))
	copy(cp, r.ipv6)
	return cp
}

// Count returns the number of IPs in the pool.
func (r *Rotator) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.all)
}
