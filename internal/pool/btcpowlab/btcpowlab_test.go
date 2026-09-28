package btcpowlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/pool"
)

func TestFetchNormalisesMinerAndPoolStats(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/public/v1/miner/bc1qtest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hashrate_5m_hs":1250000000000,"accepted_shares":42,"rejected":3,"best_share_difficulty":"12345.5","last_share_at":1790559888.25,"workers_online":1,"workers":[{"name":"gamma","hashrate_5m_hs":1250000000000,"last_share_at":1790559888.25}]}`))
	})
	mux.HandleFunc("/public/v1/pool", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pool":{"active_miners":9,"hashrate_5m_ths":70.25}}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := New(Config{BaseURL: server.URL, Timeout: 2 * time.Second})
	snap, err := adapter.Fetch(context.Background(), pool.Input{Miners: []pool.Miner{{Name: "Gamma", Address: "bc1qtest"}}})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Provider != pool.KeyBTCPoWLab || snap.HashrateTHs == nil || *snap.HashrateTHs != 1.25 {
		t.Fatalf("unexpected provider or hashrate: %#v", snap)
	}
	if snap.AcceptedShares == nil || *snap.AcceptedShares != 42 || snap.RejectedShares == nil || *snap.RejectedShares != 3 {
		t.Fatalf("unexpected shares: %#v", snap)
	}
	if snap.BestDifficulty == nil || *snap.BestDifficulty != 12345.5 || snap.ActiveWorkers == nil || *snap.ActiveWorkers != 1 {
		t.Fatalf("unexpected best or workers: %#v", snap)
	}
	if snap.PoolMiners == nil || *snap.PoolMiners != 9 || snap.PoolHashrateTHs == nil || *snap.PoolHashrateTHs != 70.25 {
		t.Fatalf("unexpected pool totals: %#v", snap)
	}
	if len(snap.Workers) != 1 || snap.Workers[0].Name != "gamma" || snap.Workers[0].HashrateTHs == nil || *snap.Workers[0].HashrateTHs != 1.25 {
		t.Fatalf("unexpected workers: %#v", snap.Workers)
	}
}

func TestFetchTreatsUnknownAddressAsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/public/v1/pool" {
			_, _ = w.Write([]byte(`{"pool":{}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	snap, err := New(Config{BaseURL: server.URL}).Fetch(context.Background(), pool.Input{Miners: []pool.Miner{{Address: "bc1qunknown"}}})
	if err != nil {
		t.Fatal(err)
	}
	if snap.WorkersCount != 0 || snap.ActiveWorkers == nil || *snap.ActiveWorkers != 0 {
		t.Fatalf("unexpected empty snapshot: %#v", snap)
	}
}
