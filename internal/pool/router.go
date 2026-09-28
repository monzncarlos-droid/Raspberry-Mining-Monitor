package pool

import (
	"context"
	"sort"
	"time"

	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/miner"
)

// RouterMiner is one configured miner as the router knows it, before any
// telemetry: its name, the payout address it mines to, and an optional explicit
// provider override ("" or "auto" means detect from the stratum host).
type RouterMiner struct {
	Name     string
	Address  string
	Override string
}

// externalNeedsAddress lists the providers that query an external API keyed by
// payout address. A miner with no address cannot use them and falls back to the
// generic (telemetry) provider.
var externalNeedsAddress = map[string]bool{
	KeyPublicPool: true,
	KeyCKPool:     true,
	KeyBraiins:    true,
	KeyBTCPoWLab:  true,
}

// Router groups miners by their detected or overridden provider and merges the
// per-provider snapshots into one. It is the only place that knows more than
// one provider exists; the rest of the app sees a single Snapshot.
type Router struct {
	miners    []RouterMiner
	providers map[string]Provider // always includes KeyGeneric
}

// NewRouter builds a router over a miner set and the available providers. The
// generic provider is added if the caller did not supply one.
func NewRouter(miners []RouterMiner, providers map[string]Provider) *Router {
	p := make(map[string]Provider, len(providers)+1)
	for k, v := range providers {
		p[k] = v
	}
	if _, ok := p[KeyGeneric]; !ok {
		p[KeyGeneric] = NewGeneric()
	}
	return &Router{miners: miners, providers: p}
}

// Name identifies the fetcher.
func (r *Router) Name() string { return "router" }

// Capabilities is the union across every available provider, since any of them
// may contribute depending on where the miners point.
func (r *Router) Capabilities() Capabilities {
	caps := Capabilities{}
	for _, p := range r.providers {
		caps = caps.Union(p.Capabilities())
	}
	return caps
}

// resolve picks the provider key for one miner.
func (r *Router) resolve(m RouterMiner, tel miner.Snapshot, hasTel bool) string {
	key := m.Override
	if key == "" || key == "auto" {
		if hasTel {
			key = Detect(tel.PoolURL)
		} else {
			key = KeyGeneric
		}
	}
	if externalNeedsAddress[key] && m.Address == "" {
		key = KeyGeneric
	}
	if _, ok := r.providers[key]; !ok {
		// Detected/overridden provider is not configured (e.g. Braiins without
		// a token): fall back to telemetry rather than dropping the miner.
		key = KeyGeneric
	}
	return key
}

// Fetch groups the miners by provider, fetches each group, and merges. It fails
// only when every contributing provider fails.
func (r *Router) Fetch(ctx context.Context, telemetry map[string]miner.Snapshot) (Snapshot, error) {
	groups := make(map[string][]Miner)
	for _, rm := range r.miners {
		tel, hasTel := telemetry[rm.Name]
		key := r.resolve(rm, tel, hasTel)
		groups[key] = append(groups[key], Miner{
			Name:         rm.Name,
			Address:      rm.Address,
			Telemetry:    tel,
			HasTelemetry: hasTel,
		})
	}

	// Deterministic order so "mixed" merges are stable.
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var (
		parts   []Snapshot
		lastErr error
	)
	for _, key := range keys {
		p := r.providers[key]
		if p == nil {
			continue
		}
		snap, err := p.Fetch(ctx, Input{Miners: groups[key]})
		if err != nil {
			lastErr = err
			continue
		}
		parts = append(parts, snap)
	}

	if len(parts) == 0 {
		if lastErr == nil {
			lastErr = errNoProviders
		}
		return Snapshot{}, lastErr
	}
	return merge(parts), nil
}

// merge folds several per-provider snapshots into one, per the agreed rules:
// sums for counts and hashrate, max for difficulties, latest for last share,
// workers concatenated with their provider label. Pool-wide figures are carried
// through only when a single provider contributed, since summing them across
// different pools is meaningless.
func merge(parts []Snapshot) Snapshot {
	out := Snapshot{}
	caps := Capabilities{}
	providers := map[string]bool{}

	for _, p := range parts {
		caps = caps.Union(p.Caps)
		providers[p.Provider] = true

		for _, w := range p.Workers {
			if w.Provider == "" {
				w.Provider = p.Provider
			}
			out.Workers = append(out.Workers, w)
		}
		out.WorkersCount += p.WorkersCount
		out.ActiveWorkers = sumPtrInt(out.ActiveWorkers, p.ActiveWorkers)
		out.HashrateTHs = sumPtrFloat(out.HashrateTHs, p.HashrateTHs)
		out.AcceptedShares = sumPtrU64(out.AcceptedShares, p.AcceptedShares)
		out.RejectedShares = sumPtrU64(out.RejectedShares, p.RejectedShares)
		out.BestDifficulty = maxPtrFloat(out.BestDifficulty, p.BestDifficulty)
		out.BestEver = maxPtrFloat(out.BestEver, p.BestEver)
		out.PoolDifficulty = maxPtrFloat(out.PoolDifficulty, p.PoolDifficulty)
		out.LastShare = laterPtr(out.LastShare, p.LastShare)
	}

	out.Caps = caps
	if len(providers) == 1 {
		out.Provider = parts[0].Provider
		// Pool-wide figures are only meaningful from a single pool.
		out.PoolHashrateTHs = parts[0].PoolHashrateTHs
		out.PoolMiners = parts[0].PoolMiners
		out.BlocksFound = parts[0].BlocksFound
	} else {
		out.Provider = "mixed"
	}

	out.Succeed(time.Now())
	return out
}
