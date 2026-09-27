// Package btcpowlab is the read only adapter for BTC PoW Lab.
// It reads the compact public miner summary, once per payout address, and
// normalises the result for the monitor dashboard.
package btcpowlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/model"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/pool"
)

const (
	defaultBaseURL = "https://btcpowlab-pool.com"
	maxBody        = 1 << 20
	hsPerTHs       = 1e12
)

type Config struct {
	BaseURL string
	Timeout time.Duration
}

type Adapter struct {
	baseURL string
	http    *http.Client
}

func New(cfg Config) *Adapter {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	return &Adapter{
		baseURL: base,
		http: &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second},
		},
	}
}

func (a *Adapter) Name() string { return pool.KeyBTCPoWLab }

func (a *Adapter) Capabilities() pool.Capabilities {
	return pool.Caps(
		pool.FieldHashrate,
		pool.FieldAcceptedShares,
		pool.FieldRejectedShares,
		pool.FieldBestShare,
		pool.FieldLastShare,
		pool.FieldActiveWorkers,
	)
}

type summaryResp struct {
	Connected           bool    `json:"connected"`
	ActiveSessions      int     `json:"active_sessions"`
	Hashrate1hHS        float64 `json:"hashrate_1h_hs"`
	Hashrate5mHS        float64 `json:"hashrate_5m_hs"`
	AcceptedShares      uint64  `json:"accepted_shares"`
	RejectedShares      uint64  `json:"rejected_shares"`
	BestShareDifficulty float64 `json:"best_share_difficulty,string"`
	LastShareAt         float64 `json:"last_share_at"`
	Workers             []struct {
		Name           string  `json:"name"`
		Hashrate1hHS   float64 `json:"hashrate_1h_hs"`
		Hashrate5mHS   float64 `json:"hashrate_5m_hs"`
		AcceptedShares uint64  `json:"accepted_shares"`
		RejectedShares uint64  `json:"rejected_shares"`
		LastShareAt    float64 `json:"last_share_at"`
	} `json:"workers"`
}

type addrResult struct {
	miner pool.Miner
	resp  *summaryResp
	err   error
}

func (a *Adapter) Fetch(ctx context.Context, in pool.Input) (pool.Snapshot, error) {
	results := make([]addrResult, len(in.Miners))
	var wg sync.WaitGroup
	for i, m := range in.Miners {
		wg.Add(1)
		go func(i int, m pool.Miner) {
			defer wg.Done()
			results[i] = a.fetchAddress(ctx, m)
		}(i, m)
	}
	wg.Wait()

	snap := pool.Snapshot{Provider: pool.KeyBTCPoWLab, Caps: a.Capabilities()}
	var (
		anyOK         bool
		lastErr       error
		hashrateTHs   float64
		accepted      uint64
		rejected      uint64
		bestShare     float64
		haveBest      bool
		latest        time.Time
		activeWorkers int
	)

	for _, r := range results {
		if r.err != nil {
			lastErr = r.err
			continue
		}
		anyOK = true
		u := r.resp
		h := u.Hashrate1hHS
		if h == 0 {
			h = u.Hashrate5mHS
		}
		hashrateTHs += h / hsPerTHs
		accepted += u.AcceptedShares
		rejected += u.RejectedShares
		if u.BestShareDifficulty > 0 && (!haveBest || u.BestShareDifficulty > bestShare) {
			bestShare, haveBest = u.BestShareDifficulty, true
		}
		if u.LastShareAt > 0 {
			ts := unixFloat(u.LastShareAt)
			if ts.After(latest) {
				latest = ts
			}
		}
		activeWorkers += u.ActiveSessions

		for _, w := range u.Workers {
			worker := pool.Worker{Name: w.Name, MinerName: r.miner.Name, Provider: pool.KeyBTCPoWLab}
			wh := w.Hashrate1hHS
			if wh == 0 {
				wh = w.Hashrate5mHS
			}
			ths := wh / hsPerTHs
			worker.HashrateTHs = &ths
			if w.LastShareAt > 0 {
				worker.LastSeen = unixFloat(w.LastShareAt)
			}
			snap.Workers = append(snap.Workers, worker)
			snap.WorkersCount++
		}
	}

	if !anyOK {
		if lastErr == nil {
			lastErr = fmt.Errorf("btcpowlab: no addresses configured")
		}
		return pool.Snapshot{}, lastErr
	}

	snap.HashrateTHs = &hashrateTHs
	snap.AcceptedShares = &accepted
	snap.RejectedShares = &rejected
	snap.ActiveWorkers = &activeWorkers
	if haveBest {
		snap.BestDifficulty = &bestShare
	}
	if !latest.IsZero() {
		snap.LastShare = &latest
	}
	snap.Source = model.Source{}
	snap.Succeed(time.Now())
	return snap, nil
}

func (a *Adapter) fetchAddress(ctx context.Context, m pool.Miner) addrResult {
	path := "/public/v1/miner/" + url.PathEscape(m.Address) + "/summary"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+path, nil)
	if err != nil {
		return addrResult{miner: m, err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "raspberry-mining-monitor")
	resp, err := a.http.Do(req)
	if err != nil {
		return addrResult{miner: m, err: fmt.Errorf("btcpowlab %s: %w", m.Name, err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return addrResult{miner: m, err: fmt.Errorf("btcpowlab %s: status %d", m.Name, resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return addrResult{miner: m, err: fmt.Errorf("btcpowlab %s: read: %w", m.Name, err)}
	}
	var summary summaryResp
	if err := json.Unmarshal(body, &summary); err != nil {
		return addrResult{miner: m, err: fmt.Errorf("btcpowlab %s: parse: %w", m.Name, err)}
	}
	return addrResult{miner: m, resp: &summary}
}

func unixFloat(seconds float64) time.Time {
	whole := int64(seconds)
	nanos := int64((seconds - float64(whole)) * 1e9)
	return time.Unix(whole, nanos)
}
