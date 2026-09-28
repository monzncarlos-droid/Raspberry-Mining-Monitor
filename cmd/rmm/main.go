// Command rmm is the Raspberry Mining Monitor backend.
//
// It polls AxeOS miners, a solo pool and a Bitcoin data source, and serves a
// read-only dashboard. It is monitoring infrastructure only: it never sits in
// the mining path, and it has no code path that writes to a miner.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/aggregate"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/alert"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/bitcoin"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/bitcoin/mempool"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/collect"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/config"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/dashboard"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/demo"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/history"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/httpapi"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/miner"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/miner/axeos"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/minercfg"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/pool"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/pool/braiins"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/pool/btcpowlab"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/pool/ckpool"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/pool/publicpool"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/push"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/settings"
	"github.com/YorkStack/Raspberry-Mining-Monitor/internal/state"
	"github.com/YorkStack/Raspberry-Mining-Monitor/web"
)

// version is the semantic version of the build. It can be overridden at build
// time with -ldflags "-X main.version=...". Bump it on every change: the patch
// digit for small fixes, the minor digit for features or notable changes.
var version = "0.15.0"

// gitRev is embedded at build time with -ldflags "-X main.gitRev=...". It is for
// log traceability only and is not shown in the UI.
var gitRev = "unknown"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "rmm:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("config", "config.yaml", "path to the configuration file")
		demoMode    = flag.Bool("demo", false, "run with simulated miners, pool and network data")
		addrFlag    = flag.String("addr", "", "override the listen address, for example 127.0.0.1:8080")
		logLevel    = flag.String("log-level", "info", "log level: debug, info, warn, error")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("rmm", version)
		return nil
	}

	log := newLogger(*logLevel)

	cfg, err := loadConfig(*configPath, *demoMode)
	if err != nil {
		return err
	}

	store := state.New(nil)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Editable miner/provider config, seeded from the loaded config on first
	// run, then owned by the admin UI.
	minersPath := filepath.Join(filepath.Dir(cfg.Dashboard.SettingsPath), "miners.json")
	minerStore := minercfg.New(minersPath, minercfg.Providers{
		BitcoinBaseURL: cfg.Bitcoin.BaseURL, PoolBaseURL: cfg.Pool.BaseURL,
	})
	if err := minerStore.SeedIfEmpty(seedSpecs(cfg), minercfg.Providers{
		BitcoinBaseURL: cfg.Bitcoin.BaseURL, PoolBaseURL: cfg.Pool.BaseURL,
	}); err != nil {
		log.Warn("could not seed miner config", "err", err)
	}

	manager := collect.NewManager(ctx, collect.ManagerOptions{
		Store:        store,
		Log:          log,
		Factories:    factories(cfg, time.Now),
		PoolInterval: cfg.Pool.Interval, PoolTimeout: cfg.Pool.Timeout,
		NetInterval: cfg.Bitcoin.Interval, NetTimeout: cfg.Bitcoin.Timeout,
	})
	manager.Reload(minerStore.Miners(), minerStore.Providers())

	// Rolling history for the charts. A load failure is non-fatal.
	hist := history.New(filepath.Join(filepath.Dir(cfg.Dashboard.SettingsPath), "history.gob"))
	if err := hist.Load(); err != nil {
		log.Warn("could not load history, starting fresh", "err", err)
	}
	go recordHistory(ctx, hist, store, log)

	addr := *addrFlag
	if addr == "" {
		addr = net.JoinHostPort(cfg.Dashboard.Bind, strconv.Itoa(cfg.Dashboard.Port))
	}

	// Threshold overrides. A broken or missing file must never stop the
	// dashboard, so a load failure is logged and the defaults stay in force.
	bands := settings.New(cfg.Dashboard.SettingsPath, defaultBand(cfg))
	if err := bands.Load(); err != nil {
		log.Warn("could not load threshold overrides, using defaults", "err", err)
	}
	// Seed the burn-in saver from the static config, unless the operator has
	// already set it via the admin page.
	saverMode := "floating"
	if cfg.Dashboard.ScreensaverMinutes == 0 {
		saverMode = "off"
	}
	bands.SetScreensaverDefault(settings.Screensaver{Mode: saverMode, Minutes: cfg.Dashboard.ScreensaverMinutes})

	// Opt-in operator alerts (miner offline / over-temperature) to a webhook.
	go runAlerts(ctx, cfg.Alerts, store, bands, log)

	opts := httpapi.Options{
		Store:              store,
		Intervals:          intervals(cfg),
		Static:             web.Assets(),
		Version:            version,
		Settings:           bands,
		SettingsEnabled:    cfg.Dashboard.Settings,
		MetricsEnabled:     cfg.Dashboard.Metrics,
		ScreensaverSeconds: cfg.Dashboard.ScreensaverMinutes * 60,
		MinerCfg:           minerStore,
		OnConfigChange: func() {
			manager.Reload(minerStore.Miners(), minerStore.Providers())
		},
		History: hist,
	}
	handler := httpapi.NewHandler(opts)

	// Optional outbound push of the fleet metrics to a Prometheus remote_write
	// endpoint (e.g. Grafana Cloud). Outbound only: the miners and this monitor
	// are never exposed. Reuses the exact view the API and /metrics serve.
	if cfg.Grafana.Enabled {
		if err := startGrafanaPush(ctx, cfg.Grafana, opts.View, version, log); err != nil {
			return err
		}
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		// No WriteTimeout: SSE connections are long-lived by design.
		IdleTimeout: 120 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	log.Info("dashboard listening",
		"addr", ln.Addr().String(),
		"demo", cfg.Demo,
		"miners", len(cfg.Miners),
		"version", version,
		"rev", gitRev)
	if cfg.Dashboard.Settings {
		log.Info("threshold settings page available from this machine only",
			"path", "/settings", "overrides", bands.Path())
	}
	if cfg.Demo {
		log.Info("demo mode: all miner, pool and network data is simulated")
	}

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		stop()
		manager.Wait()
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("graceful shutdown failed", "err", err)
	}
	manager.Wait()
	return nil
}

// startGrafanaPush resolves the token (inline or from token_file) and starts
// the remote_write loop. A missing token file is a hard error, since the
// operator explicitly enabled the push.
func startGrafanaPush(ctx context.Context, g config.Grafana, view func() dashboard.View, version string, log *slog.Logger) error {
	token := g.Token
	if g.TokenFile != "" {
		b, err := os.ReadFile(g.TokenFile)
		if err != nil {
			return fmt.Errorf("grafana token_file: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	pusher := push.New(push.Config{
		URL:      g.URL,
		User:     g.User,
		Token:    token,
		Interval: g.Interval,
		Timeout:  g.Timeout,
		Version:  version,
	}, log)
	go push.Run(ctx, pusher, view)
	log.Info("grafana metrics push enabled", "url", g.URL, "interval", g.Interval)
	return nil
}

func loadConfig(path string, demoMode bool) (config.Config, error) {
	if demoMode {
		return config.Demo(), nil
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, fmt.Errorf("%w\n\nRun with --demo to start without miners", err)
	}
	return cfg, nil
}

// defaultBand is the fleet-wide warning band taken from the first configured
// miner, which is where the config defaults land.
func defaultBand(cfg config.Config) settings.Thresholds {
	b := settings.Thresholds{
		ASICWarnC: config.DefaultWarnTempC,
		ASICCritC: config.DefaultCritTempC,
		VRMWarnC:  config.DefaultVRMWarnTempC,
		VRMCritC:  config.DefaultVRMCritTempC,
	}
	if len(cfg.Miners) > 0 {
		m := cfg.Miners[0]
		b = settings.Thresholds{
			ASICWarnC: m.WarnTempC,
			ASICCritC: m.CritTempC,
			VRMWarnC:  m.VRMWarnTempC,
			VRMCritC:  m.VRMCritTempC,
		}
	}
	return b
}

func intervals(cfg config.Config) dashboard.Input {
	in := dashboard.Input{
		MinerInterval:   config.DefaultMinerInterval,
		PoolInterval:    cfg.Pool.Interval,
		NetworkInterval: cfg.Bitcoin.Interval,
		Thresholds: dashboard.Thresholds{
			ASICWarnC: config.DefaultWarnTempC,
			ASICCritC: config.DefaultCritTempC,
			VRMWarnC:  config.DefaultVRMWarnTempC,
			VRMCritC:  config.DefaultVRMCritTempC,
		},
	}
	// Staleness uses the slowest configured miner interval so a deliberately
	// slow-polled device is not permanently flagged as stale.
	for _, m := range cfg.Miners {
		if m.Interval > in.MinerInterval {
			in.MinerInterval = m.Interval
		}
	}
	return in
}

// recordHistory samples the current fleet totals every 10 s into the rolling
// history, and persists the rings every 5 minutes plus once on shutdown. It
// only records while at least one miner is online, so downtime does not draw a
// misleading flat line at zero.
// runAlerts evaluates alert conditions against the live fleet once a minute and
// delivers any alerts to the configured webhook. It returns immediately when no
// webhook is configured, so alerts are strictly opt-in.
func runAlerts(ctx context.Context, cfg config.Alerts, store *state.Store, bands *settings.Store, log *slog.Logger) {
	if cfg.WebhookURL == "" {
		return
	}
	engine := alert.New(alert.Config{
		OfflineAfter: time.Duration(cfg.OfflineMinutes) * time.Minute,
		TempAlerts:   cfg.TempAlerts,
		Cooldown:     time.Duration(cfg.CooldownMinutes) * time.Minute,
	})
	notifier := alert.NewWebhook(cfg.WebhookURL, 8*time.Second)
	log.Info("operator alerts enabled", "offlineMinutes", cfg.OfflineMinutes, "tempAlerts", cfg.TempAlerts)

	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			def := bands.Default()
			ov := bands.Overrides()
			statuses := make([]alert.MinerStatus, 0)
			for _, m := range store.Miners() {
				crit := def.ASICCritC
				if t, ok := ov[m.Name]; ok {
					crit = t.ASICCritC
				}
				st := alert.MinerStatus{Name: m.Name, Online: m.OK, ASICTempC: m.ASICTempC, CritTempC: crit}
				if !m.OK {
					st.OfflineFor = now.Sub(m.FetchedAt)
				}
				statuses = append(statuses, st)
			}
			for _, a := range engine.Evaluate(now, statuses) {
				sendCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
				if err := notifier.Notify(sendCtx, a); err != nil {
					log.Warn("alert delivery failed", "miner", a.Miner, "kind", a.Kind, "err", err)
				}
				cancel()
			}
		}
	}
}

// ptrOr returns the pointed-to value, or 0 when the pointer is nil. A miner
// that does not report power or temperature records 0 for that series.
func ptrOr(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func recordHistory(ctx context.Context, hist *history.Store, store *state.Store, log *slog.Logger) {
	sample := time.NewTicker(10 * time.Second)
	defer sample.Stop()
	persist := time.NewTicker(5 * time.Minute)
	defer persist.Stop()

	for {
		select {
		case <-ctx.Done():
			if err := hist.Save(); err != nil {
				log.Warn("could not save history on shutdown", "err", err)
			}
			return
		case now := <-sample.C:
			miners := store.Miners()
			agg := make([]aggregate.MinerInput, 0, len(miners))
			perMiner := make(map[string]history.MinerSample, len(miners))
			for _, m := range miners {
				agg = append(agg, aggregate.MinerInput{Name: m.Name, OK: m.OK, HashrateTHs: m.HashrateTHs, PowerW: m.PowerW})
				if m.OK {
					perMiner[m.Name] = history.MinerSample{
						Hashrate: m.HashrateTHs,
						Power:    ptrOr(m.PowerW),
						Temp:     ptrOr(m.ASICTempC),
					}
				}
			}
			t := aggregate.Combine(agg)
			if t.MinersOnline == 0 {
				continue
			}
			hist.Record(now, history.Sample{
				Hashrate: t.HashrateTHs,
				Power:    t.PowerW,
				Price:    store.Network().PriceEUR,
			})
			hist.RecordMiners(now, perMiner)
		case <-persist.C:
			if err := hist.Save(); err != nil {
				log.Warn("could not persist history", "err", err)
			}
		}
	}
}

// seedSpecs converts the loaded config's miners into editable specs for the
// first-run seed of the miner config store.
// buildRouter wires the provider-agnostic pool router: publicpool, ckpool and
// the generic telemetry fallback always, plus braiins when a token is set. Each
// miner is routed by its explicit override, the global default, or (when the
// default is "auto") detection from its stratum host.
func buildRouter(cfg config.Config, specs []minercfg.Spec, prov minercfg.Providers) pool.Fetcher {
	defaultOverride := cfg.Pool.Provider
	if defaultOverride == "auto" {
		defaultOverride = ""
	}
	routerMiners := make([]pool.RouterMiner, 0, len(specs))
	for _, m := range specs {
		ov := m.PoolProvider
		if ov == "" {
			ov = defaultOverride
		}
		routerMiners = append(routerMiners, pool.RouterMiner{
			Name:     m.Name,
			Address:  m.PayoutAddress,
			Override: ov,
		})
	}
	if len(routerMiners) == 0 {
		return nil
	}

	providers := map[string]pool.Provider{
		pool.KeyPublicPool: publicpool.New(publicpool.Config{BaseURL: prov.PoolBaseURL, Timeout: cfg.Pool.Timeout}),
		pool.KeyCKPool:     ckpool.New(ckpool.Config{Timeout: cfg.Pool.Timeout}),
		pool.KeyBTCPoWLab:  btcpowlab.New(btcpowlab.Config{Timeout: cfg.Pool.Timeout}),
		pool.KeyGeneric:    pool.NewGeneric(),
	}
	if cfg.Pool.Token != "" {
		providers[pool.KeyBraiins] = braiins.New(braiins.Config{Token: cfg.Pool.Token, Timeout: cfg.Pool.Timeout})
	}
	return pool.NewRouter(routerMiners, providers)
}

func seedSpecs(cfg config.Config) []minercfg.Spec {
	out := make([]minercfg.Spec, 0, len(cfg.Miners))
	for _, m := range cfg.Miners {
		out = append(out, minercfg.Spec{
			Name:          m.Name,
			Type:          m.Type,
			Host:          m.Host,
			PayoutAddress: m.PayoutAddress,
			PoolProvider:  m.PoolProvider,
			Token:         m.Token,
			Interval:      m.Interval,
			Timeout:       m.Timeout,
			NominalTHs:    m.NominalTHs,
			NominalW:      m.NominalW,
			NominalTempC:  m.NominalTempC,
			Model:         m.Model,
			Fans:          m.Fans,
		})
	}
	return out
}

// seedFromName gives each demo miner a stable seed so its simulated data does
// not jump around when the miner list is reloaded.
func seedFromName(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

// factories builds the concrete collectors the manager needs. The provider
// TYPE (demo vs real) stays from the loaded config; the manager supplies the
// live miner list and provider URLs.
func factories(cfg config.Config, clock func() time.Time) collect.Factories {
	return collect.Factories{
		Miner: func(spec minercfg.Spec) miner.Collector {
			switch spec.Type {
			case "demo":
				return demo.NewMiner(demo.MinerConfig{
					Name:          spec.Name,
					Model:         spec.Model,
					Firmware:      "demo",
					NominalTHs:    spec.NominalTHs,
					NominalW:      spec.NominalW,
					NominalTempC:  spec.NominalTempC,
					Fans:          spec.Fans,
					DropoutChance: 0.004,
				}, seedFromName(spec.Name), clock)
			default: // axeos
				return axeos.New(axeos.Config{
					Name:    spec.Name,
					BaseURL: minerBaseURL(spec.Host),
					Timeout: spec.Timeout,
					Token:   spec.Token,
				})
			}
		},
		Pool: func(specs []minercfg.Spec, prov minercfg.Providers) pool.Fetcher {
			switch cfg.Pool.Provider {
			case "none":
				return nil
			case "demo":
				names := make([]string, len(specs))
				for i, m := range specs {
					names[i] = m.Name
				}
				return demo.NewPool(names, 42, clock)
			}
			return buildRouter(cfg, specs, prov)
		},
		Bitcoin: func(prov minercfg.Providers) bitcoin.Provider {
			switch cfg.Bitcoin.Provider {
			case "demo":
				return demo.NewBitcoin(7, clock)
			case "public":
				return mempool.New(mempool.Config{BaseURL: prov.BitcoinBaseURL, Timeout: cfg.Bitcoin.Timeout})
			default:
				return nil
			}
		},
	}
}

// minerBaseURL turns a configured host into an AxeOS base URL. A bare host or
// IP gets http:// and the default port; an explicit scheme is left alone.
func minerBaseURL(host string) string {
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return strings.TrimRight(host, "/")
	}
	return "http://" + host
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}
