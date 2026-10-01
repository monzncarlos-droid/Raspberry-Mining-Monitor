// Package btcpowlab is the read only adapter for BTC PoW Lab Hybrid Solo.
package btcpowlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
	return &Adapter{baseURL: base, http: &http.Client{Timeout: timeout}}
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

type minerResp struct {
	Hashrate5mHS        float64  `json:"hashrate_5m_hs"`
	AcceptedShares      uint64   `json:"accepted_shares"`
	Rejected            uint64   `json:"rejected_shares"`
	BestShareDifficulty string   `json:"best_share_difficulty"`
	LastShareAt         *float64 `json:"last_share_at"`
	Workers             []struct {
		Name         string   `json:"name"`
		Hashrate5mHS float64  `json:"hashrate_5m_hs"`
		LastShareAt  *float64 `json:"last_share_at"`
	} `json:"workers"`
}

type poolResp struct {
	Pool struct {
		ActiveMiners  int     `json:"active_miners"`
		Hashrate5mTHs float64 `json:"hashrate_5m_ths"`
	} `json:"pool"`
}

type addrResult struct {
	miner    pool.Miner
	resp     *minerResp
	err      error
	notFound bool
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
	var anyOK bool
	var lastErr error
	var hashrate float64
	var accepted, rejected uint64
	var best float64
	var haveBest bool
	var latest time.Time
	var active int

	for _, result := range results {
		if result.err != nil {
			lastErr = result.err
			continue
		}
		if result.notFound {
			anyOK = true
			continue
		}
		anyOK = true
		m := result.resp
		hashrate += m.Hashrate5mHS / hsPerTHs
		accepted += m.AcceptedShares
		rejected += m.Rejected
		active += len(m.Workers)
		if value, ok := parseFloat(m.BestShareDifficulty); ok && (!haveBest || value > best) {
			best, haveBest = value, true
		}
		if ts := unixTime(m.LastShareAt); ts.After(latest) {
			latest = ts
		}
		for _, worker := range m.Workers {
			w := pool.Worker{Name: worker.Name, MinerName: result.miner.Name, Provider: pool.KeyBTCPoWLab}
			ths := worker.Hashrate5mHS / hsPerTHs
			w.HashrateTHs = &ths
			w.LastSeen = unixTime(worker.LastShareAt)
			snap.Workers = append(snap.Workers, w)
		}
		snap.WorkersCount += len(m.Workers)
	}

	if !anyOK {
		if lastErr == nil {
			lastErr = fmt.Errorf("btcpowlab: no addresses configured")
		}
		return pool.Snapshot{}, lastErr
	}
	snap.HashrateTHs = &hashrate
	snap.AcceptedShares = &accepted
	snap.RejectedShares = &rejected
	snap.ActiveWorkers = &active
	if haveBest {
		snap.BestDifficulty = &best
	}
	if !latest.IsZero() {
		snap.LastShare = &latest
	}
	if pr, err := a.fetchPool(ctx); err == nil {
		snap.PoolMiners = &pr.Pool.ActiveMiners
		snap.PoolHashrateTHs = &pr.Pool.Hashrate5mTHs
	}
	snap.Source = model.Source{}
	snap.Succeed(time.Now())
	return snap, nil
}

func (a *Adapter) fetchAddress(ctx context.Context, m pool.Miner) addrResult {
	status, body, err := a.get(ctx, "/public/v1/miner/"+url.PathEscape(m.Address)+"/summary")
	if err != nil {
		return addrResult{miner: m, err: err}
	}
	if status == http.StatusNotFound {
		return addrResult{miner: m, notFound: true}
	}
	if status != http.StatusOK {
		return addrResult{miner: m, err: fmt.Errorf("btcpowlab %s: status %d", m.Name, status)}
	}
	var mr minerResp
	if err := json.Unmarshal(body, &mr); err != nil {
		return addrResult{miner: m, err: fmt.Errorf("btcpowlab %s: parse: %w", m.Name, err)}
	}
	return addrResult{miner: m, resp: &mr}
}

func (a *Adapter) fetchPool(ctx context.Context) (poolResp, error) {
	var pr poolResp
	status, body, err := a.get(ctx, "/public/v1/pool")
	if err != nil {
		return pr, err
	}
	if status != http.StatusOK {
		return pr, fmt.Errorf("btcpowlab: pool status %d", status)
	}
	return pr, json.Unmarshal(body, &pr)
}

func (a *Adapter) get(ctx context.Context, path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "raspberry-mining-monitor")
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("btcpowlab: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func parseFloat(raw string) (float64, bool) {
	if strings.TrimSpace(raw) == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	return value, err == nil
}

func unixTime(raw *float64) time.Time {
	if raw == nil || *raw <= 0 {
		return time.Time{}
	}
	seconds := int64(*raw)
	nanos := int64((*raw - float64(seconds)) * 1e9)
	return time.Unix(seconds, nanos)
}
