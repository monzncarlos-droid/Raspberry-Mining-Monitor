// Package config loads and validates the YAML configuration.
//
// Miner addresses live here and nowhere else. Nothing in the source carries a
// built-in IP.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults applied when a value is omitted.
const (
	DefaultPort            = 8080
	DefaultBind            = "0.0.0.0"
	DefaultAdminBind       = "127.0.0.1"
	DefaultMinerInterval   = 2 * time.Second
	DefaultPoolInterval    = 60 * time.Second
	DefaultBitcoinInterval = 30 * time.Second
	DefaultTimeout         = 8 * time.Second
	DefaultMinerTimeout    = 2 * time.Second
	DefaultBitcoinBaseURL  = "https://mempool.space"
	DefaultPoolBaseURL     = "https://public-pool.io:40557"
	// Both AxeOS variants trigger thermal protection at 70 C: the NerdQAxe
	// fork ships overheat_temp=70, upstream ships selftest_max=70. Red sits on
	// that line, amber gives six degrees of warning first, and a NerdOctaxe
	// idling at 62 to 63 C stays green.
	DefaultWarnTempC    = 64.0
	DefaultCritTempC    = 70.0
	DefaultVRMWarnTempC = 80.0
	DefaultVRMCritTempC = 90.0
	DefaultSettingsPath = "thresholds.json"

	// DefaultScreensaverMinutes is the idle time before the burn-in saver.
	DefaultScreensaverMinutes = 15
)

// minInterval guards against a typo turning the monitor into a denial of
// service against the miners or a public API.
const minInterval = time.Second

// Miner is one configured device.
type Miner struct {
	Name string `yaml:"name"`
	Host string `yaml:"host"`
	Type string `yaml:"type"`

	// PayoutAddress is this miner's own Public Pool address. Each miner has
	// its own, so the pool collector makes one call per address.
	PayoutAddress string `yaml:"payout_address"`

	// PoolProvider overrides provider detection for this miner:
	// publicpool | ckpool | braiins | btcpowlab | generic | auto (empty = use the global default).
	PoolProvider string `yaml:"pool_provider"`

	// Token is an optional Bearer token sent on every request to this miner, for
	// miners behind the LAN monitoring security contract (e.g. the Mac metal
	// miner). It stays in config.yaml / miners.json and is never exposed through
	// the admin API or the UI.
	Token string `yaml:"token"`

	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`

	WarnTempC    float64 `yaml:"warn_temp_c"`
	CritTempC    float64 `yaml:"crit_temp_c"`
	VRMWarnTempC float64 `yaml:"vrm_warn_temp_c"`
	VRMCritTempC float64 `yaml:"vrm_crit_temp_c"`

	// Demo marks a simulated device and carries its nominal figures.
	NominalTHs   float64 `yaml:"-"`
	NominalW     float64 `yaml:"-"`
	NominalTempC float64 `yaml:"-"`
	Model        string  `yaml:"-"`
	Fans         int     `yaml:"-"`
}

// Bitcoin configures the network-data provider.
type Bitcoin struct {
	Provider string        `yaml:"provider"`
	BaseURL  string        `yaml:"base_url"`
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
}

// Pool configures the solo-pool adapter.
type Pool struct {
	// Provider is the default provider for miners without their own override:
	// publicpool | ckpool | braiins | btcpowlab | generic | none | demo | auto.
	// "auto" detects the provider from each miner's stratum host.
	Provider string `yaml:"provider"`
	BaseURL  string `yaml:"base_url"`
	// Token is the API token for providers that need one (Braiins). It is kept
	// in config.yaml only and never exposed through the admin API or the UI.
	Token    string        `yaml:"token"`
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
}

// Dashboard configures the HTTP server.
type Dashboard struct {
	Bind string `yaml:"bind"`
	Port int    `yaml:"port"`
	// AdminBind is where the health and debug endpoints listen. It defaults
	// to loopback and should stay there.
	AdminBind string `yaml:"admin_bind"`

	// Settings enables the loopback-only threshold page. Turn it off if this
	// service is ever placed behind a reverse proxy, because every request
	// would then appear to come from loopback.
	Settings bool `yaml:"settings"`

	// SettingsPath is where threshold overrides are persisted.
	SettingsPath string `yaml:"settings_path"`

	// Metrics exposes GET /metrics in Prometheus format, gated to the local
	// network. Defaults to true.
	Metrics bool `yaml:"metrics"`

	// ScreensaverMinutes is the idle time before the burn-in screensaver
	// appears on the kiosk. 0 disables it.
	ScreensaverMinutes int `yaml:"screensaver_minutes"`

	settingsSet    bool `yaml:"-"`
	screensaverSet bool `yaml:"-"`

	// portSet distinguishes an omitted port from an explicit 0. Defaulting a
	// written-out zero to 8080 would silently ignore what the operator asked
	// for, so an explicit 0 is rejected instead.
	portSet bool `yaml:"-"`
}

// UnmarshalYAML records whether the port was written out at all.
func (d *Dashboard) UnmarshalYAML(n *yaml.Node) error {
	var raw struct {
		Bind               string `yaml:"bind"`
		Port               *int   `yaml:"port"`
		AdminBind          string `yaml:"admin_bind"`
		Settings           *bool  `yaml:"settings"`
		Metrics            *bool  `yaml:"metrics"`
		SettingsPath       string `yaml:"settings_path"`
		ScreensaverMinutes *int   `yaml:"screensaver_minutes"`
	}
	if err := n.Decode(&raw); err != nil {
		return err
	}
	d.Bind = raw.Bind
	d.AdminBind = raw.AdminBind
	d.SettingsPath = raw.SettingsPath
	d.Settings = raw.Settings == nil || *raw.Settings
	d.settingsSet = true
	d.Metrics = raw.Metrics == nil || *raw.Metrics
	if raw.ScreensaverMinutes != nil {
		d.ScreensaverMinutes = *raw.ScreensaverMinutes
		d.screensaverSet = true
	}
	if raw.Port != nil {
		d.Port = *raw.Port
		d.portSet = true
	}
	return nil
}

// Alerts configures opt-in operator notifications. Everything is off unless a
// webhook URL is set. The URL is a secret held in this file only; it is never
// exposed through the API or the UI.
type Alerts struct {
	WebhookURL      string `yaml:"webhook_url"`
	OfflineMinutes  int    `yaml:"offline_minutes"`
	TempAlerts      bool   `yaml:"temp_alerts"`
	CooldownMinutes int    `yaml:"cooldown_minutes"`
}

// History configures the local sample store.
type History struct {
	Enabled       bool   `yaml:"enabled"`
	Path          string `yaml:"path"`
	RetentionDays int    `yaml:"retention_days"`
}

// Grafana configures optional push of the fleet metrics to a
// Prometheus-compatible remote_write endpoint, such as Grafana Cloud. It is off
// unless enabled with a URL. This lets the monitor forward metrics outbound only
// so the miners and the monitor are never exposed to the internet. The token is
// a secret held in this file (or in token_file) only; it is never exposed
// through the API or the UI.
type Grafana struct {
	Enabled   bool          `yaml:"enabled"`
	URL       string        `yaml:"url"`
	User      string        `yaml:"user"`
	Token     string        `yaml:"token"`
	TokenFile string        `yaml:"token_file"`
	Interval  time.Duration `yaml:"interval"`
	Timeout   time.Duration `yaml:"timeout"`
}

// Config is the whole file.
type Config struct {
	Miners    []Miner   `yaml:"miners"`
	Bitcoin   Bitcoin   `yaml:"bitcoin"`
	Pool      Pool      `yaml:"pool"`
	Dashboard Dashboard `yaml:"dashboard"`
	Alerts    Alerts    `yaml:"alerts"`
	History   History   `yaml:"history"`
	Grafana   Grafana   `yaml:"grafana"`

	// Demo is set by the --demo flag, never by the file.
	Demo bool `yaml:"-"`
}

var (
	knownMinerTypes    = map[string]bool{"axeos": true, "demo": true}
	knownPoolProviders = map[string]bool{"publicpool": true, "ckpool": true, "braiins": true, "btcpowlab": true, "generic": true, "auto": true, "none": true, "demo": true}
	knownBTCProviders  = map[string]bool{"public": true, "core": true, "demo": true}
)

// Parse reads YAML, applies defaults and validates the result.
func Parse(data []byte) (Config, error) {
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Load reads and parses a config file.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

func (c *Config) applyDefaults() {
	if !c.Dashboard.portSet && c.Dashboard.Port == 0 {
		c.Dashboard.Port = DefaultPort
	}
	if c.Dashboard.Bind == "" {
		c.Dashboard.Bind = DefaultBind
	}
	if c.Dashboard.AdminBind == "" {
		c.Dashboard.AdminBind = DefaultAdminBind
	}
	if !c.Dashboard.settingsSet {
		c.Dashboard.Settings = true
		// settingsSet is false only when the dashboard section was omitted
		// entirely, in which case metrics default on too.
		c.Dashboard.Metrics = true
	}
	if c.Dashboard.SettingsPath == "" {
		c.Dashboard.SettingsPath = DefaultSettingsPath
	}
	if !c.Dashboard.screensaverSet && c.Dashboard.ScreensaverMinutes == 0 {
		c.Dashboard.ScreensaverMinutes = DefaultScreensaverMinutes
	}

	if c.Bitcoin.Provider == "" {
		c.Bitcoin.Provider = "public"
	}
	if c.Bitcoin.BaseURL == "" {
		c.Bitcoin.BaseURL = DefaultBitcoinBaseURL
	}
	if c.Bitcoin.Interval == 0 {
		c.Bitcoin.Interval = DefaultBitcoinInterval
	}
	if c.Bitcoin.Timeout == 0 {
		c.Bitcoin.Timeout = DefaultTimeout
	}

	if c.Pool.Provider == "" {
		c.Pool.Provider = "publicpool"
	}
	if c.Pool.BaseURL == "" {
		c.Pool.BaseURL = DefaultPoolBaseURL
	}
	if c.Pool.Interval == 0 {
		c.Pool.Interval = DefaultPoolInterval
	}
	if c.Pool.Timeout == 0 {
		c.Pool.Timeout = DefaultTimeout
	}

	if c.History.RetentionDays == 0 {
		c.History.RetentionDays = 7
	}

	if c.Grafana.Enabled {
		if c.Grafana.Interval == 0 {
			c.Grafana.Interval = 30 * time.Second
		}
		if c.Grafana.Timeout == 0 {
			c.Grafana.Timeout = 10 * time.Second
		}
	}

	// Alerts are opt-in: nothing runs without a webhook URL. When one is set but
	// no conditions were chosen, enable sensible defaults so it is useful at once.
	if c.Alerts.WebhookURL != "" {
		if c.Alerts.OfflineMinutes == 0 && !c.Alerts.TempAlerts {
			c.Alerts.OfflineMinutes = 10
			c.Alerts.TempAlerts = true
		}
		if c.Alerts.CooldownMinutes == 0 {
			c.Alerts.CooldownMinutes = 30
		}
	}

	for i := range c.Miners {
		m := &c.Miners[i]
		if m.Type == "" {
			m.Type = "axeos"
		}
		if m.Interval == 0 {
			m.Interval = DefaultMinerInterval
		}
		if m.Timeout == 0 {
			m.Timeout = DefaultMinerTimeout
		}
		if m.WarnTempC == 0 {
			m.WarnTempC = DefaultWarnTempC
		}
		if m.CritTempC == 0 {
			m.CritTempC = DefaultCritTempC
		}
		if m.VRMWarnTempC == 0 {
			m.VRMWarnTempC = DefaultVRMWarnTempC
		}
		if m.VRMCritTempC == 0 {
			m.VRMCritTempC = DefaultVRMCritTempC
		}
	}
}

// Validate reports the first problem that would stop the monitor from running.
func (c Config) Validate() error {
	if len(c.Miners) == 0 {
		return errors.New("config: at least one miner must be configured")
	}

	seen := make(map[string]bool, len(c.Miners))
	for i, m := range c.Miners {
		if m.Name == "" {
			return fmt.Errorf("config: miner %d has no name", i)
		}
		if seen[m.Name] {
			return fmt.Errorf("config: duplicate miner name %q", m.Name)
		}
		seen[m.Name] = true

		if !knownMinerTypes[m.Type] {
			return fmt.Errorf("config: miner %q has unknown type %q", m.Name, m.Type)
		}
		if m.Type == "axeos" && m.Host == "" {
			return fmt.Errorf("config: miner %q has no host", m.Name)
		}
		if m.Interval < minInterval {
			return fmt.Errorf("config: miner %q has interval %v, the minimum is %v", m.Name, m.Interval, minInterval)
		}
		if m.CritTempC <= m.WarnTempC {
			return fmt.Errorf("config: miner %q has crit_temp_c %v at or below warn_temp_c %v", m.Name, m.CritTempC, m.WarnTempC)
		}
	}

	if !knownPoolProviders[c.Pool.Provider] {
		return fmt.Errorf("config: unknown pool provider %q", c.Pool.Provider)
	}
	if c.Pool.Interval < minInterval {
		return fmt.Errorf("config: pool interval %v is below the minimum %v", c.Pool.Interval, minInterval)
	}
	if !knownBTCProviders[c.Bitcoin.Provider] {
		return fmt.Errorf("config: unknown bitcoin provider %q", c.Bitcoin.Provider)
	}
	if c.Bitcoin.Interval < minInterval {
		return fmt.Errorf("config: bitcoin interval %v is below the minimum %v", c.Bitcoin.Interval, minInterval)
	}

	if c.Dashboard.Port < 1 || c.Dashboard.Port > 65535 {
		return fmt.Errorf("config: dashboard port %d is out of range", c.Dashboard.Port)
	}

	if c.Grafana.Enabled {
		if c.Grafana.URL == "" {
			return errors.New("config: grafana.enabled but grafana.url is empty")
		}
		if c.Grafana.User == "" {
			return errors.New("config: grafana.enabled but grafana.user is empty")
		}
		if c.Grafana.Token == "" && c.Grafana.TokenFile == "" {
			return errors.New("config: grafana.enabled but neither grafana.token nor grafana.token_file is set")
		}
	}

	return nil
}

// Demo returns the configuration used by --demo. It describes two simulated
// miners matching the reference hardware and carries no network addresses.
func Demo() Config {
	c := Config{
		Demo: true,
		Miners: []Miner{
			{
				Name:         "NerdOctaxe",
				Type:         "demo",
				Model:        "BM1370 x6",
				NominalTHs:   12.10,
				NominalW:     158,
				NominalTempC: 62,
				Fans:         2,
			},
			{
				Name:         "Gamma 602",
				Type:         "demo",
				Model:        "BM1370",
				NominalTHs:   1.27,
				NominalW:     18,
				NominalTempC: 55,
				Fans:         1,
			},
			{
				// Experimental Metal miner on a MacBook. SHA-256 on a CPU/GPU
				// is wildly inefficient, so the tiny hashrate and huge J/TH are
				// intentional and honest, not a bug.
				Name:         "MacBook M2",
				Type:         "demo",
				Model:        "Apple M2",
				NominalTHs:   0.02,
				NominalW:     22,
				NominalTempC: 51,
				Fans:         1,
			},
		},
		Bitcoin: Bitcoin{Provider: "demo"},
		Pool:    Pool{Provider: "demo"},
	}
	c.applyDefaults()
	return c
}
