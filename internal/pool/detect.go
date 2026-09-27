package pool

import "strings"

// Provider keys. These are the only provider identifiers the rest of the system
// uses; nothing outside this package should compare against a specific pool.
const (
	KeyPublicPool = "publicpool"
	KeyCKPool     = "ckpool"
	KeyBraiins    = "braiins"
	KeyBTCPoWLab  = "btcpowlab"
	KeyGeneric    = "generic"
)

// Detect maps a miner's configured stratum host to a provider key. It reads the
// host only; the monitor never changes a miner's pool configuration.
//
// An unrecognised or empty host returns KeyGeneric, so a miner on any other
// pool still gets stats derived from its own telemetry.
func Detect(stratumURL string) string {
	host := hostOf(stratumURL)
	if host == "" {
		return KeyGeneric
	}
	switch {
	case strings.HasSuffix(host, "public-pool.io"):
		return KeyPublicPool
	case strings.HasSuffix(host, "ckpool.org"):
		return KeyCKPool
	case strings.HasSuffix(host, "braiins.com"):
		return KeyBraiins
	case strings.HasSuffix(host, "btcpowlab-pool.com"):
		return KeyBTCPoWLab
	default:
		return KeyGeneric
	}
}

// hostOf extracts a lowercase host from a stratum URL that may carry a scheme
// (stratum+tcp://), a port, or neither.
func hostOf(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// Drop any path.
	if i := strings.IndexAny(s, "/"); i >= 0 {
		s = s[:i]
	}
	// Drop a port. IPv6 literals are not expected for a pool host.
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[:i]
	}
	return s
}
