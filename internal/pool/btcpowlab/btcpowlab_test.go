package btcpowlab

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/pool"
)

const testAddress = "bc1qexampleaddress"

func TestFetchMapsPublicSummary(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/public/v1/miner/"+testAddress+"/summary", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"connected":true,"active_sessions":1,
			"hashrate_5m_hs":12500000000000,"hashrate_1h_hs":12100000000000,
			"accepted_shares":123,"rejected_shares":2,
			"best_share_difficulty":"45678.5","last_share_at":1790467728.25,
			"workers":[{"name":"garage","accepted_shares":123,"rejected_shares":2,
			"hashrate_5m_hs":12500000000000,"hashrate_1h_hs":12100000000000,
			"last_share_at":1790467728.25}]
		}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := New(Config{BaseURL: srv.URL, Timeout: 2 * time.Second})
	s, err := a.Fetch(context.Background(), pool.Input{Miners: []pool.Miner{{Name: "Garage", Address: testAddress}}})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if s.Provider != pool.KeyBTCPoWLab {
		t.Errorf("Provider = %q", s.Provider)
	}
	if s.HashrateTHs == nil || math.Abs(*s.HashrateTHs-12.1) > 1e-6 {
		t.Errorf("HashrateTHs = %v, want 12.1", s.HashrateTHs)
	}
	if s.AcceptedShares == nil || *s.AcceptedShares != 123 {
		t.Errorf("AcceptedShares = %v", s.AcceptedShares)
	}
	if s.RejectedShares == nil || *s.RejectedShares != 2 {
		t.Errorf("RejectedShares = %v", s.RejectedShares)
	}
	if s.BestDifficulty == nil || *s.BestDifficulty != 45678.5 {
		t.Errorf("BestDifficulty = %v", s.BestDifficulty)
	}
	if s.ActiveWorkers == nil || *s.ActiveWorkers != 1 {
		t.Errorf("ActiveWorkers = %v", s.ActiveWorkers)
	}
	if len(s.Workers) != 1 || s.Workers[0].Provider != pool.KeyBTCPoWLab {
		t.Errorf("Workers = %+v", s.Workers)
	}
}

func TestFetchFailsWhenEndpointFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	a := New(Config{BaseURL: srv.URL, Timeout: 2 * time.Second})
	_, err := a.Fetch(context.Background(), pool.Input{Miners: []pool.Miner{{Name: "Garage", Address: testAddress}}})
	if err == nil {
		t.Fatal("Fetch error = nil")
	}
}
