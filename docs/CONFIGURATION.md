# Configuration

There are two layers:

1. **`config.yaml`** — the startup seed, read once at launch. It sets the initial
   miners, providers, network binding and defaults.
2. **Runtime state** — after first run, miners, provider URLs and display
   preferences are edited live on the `/settings` page and stored next to the
   config path (`miners.json`, `thresholds.json`, `history.gob`). These win over
   `config.yaml` on later starts.

So `config.yaml` seeds the first boot; the admin page owns everything after that.
Running with `--demo` ignores the file entirely and uses a built-in simulated
fleet.

## config.yaml

Copy `deploy/config.example.yaml` to `config.yaml` and edit. The file is
gitignored — it is the only place addresses belong.

### miners

```yaml
miners:
  - name: NerdOctaxe
    host: 192.168.1.51        # IP or hostname of the miner's AxeOS web UI
    type: axeos               # axeos | demo
    payout_address: bc1q…
    interval: 2s              # how often to poll
    timeout: 2s
    warn_temp_c: 64           # amber at or above this
    crit_temp_c: 70           # red at or above this
```

`host` is the miner's own web UI address, reachable from the Pi. `type: axeos`
covers both upstream Bitaxe and NerdQAxe firmware; the collector detects which
one automatically. An optional `pool_provider` (`publicpool` | `ckpool` |
`braiins` | `btcpowlab` | `generic` | `auto`) overrides pool detection for that one miner.

An optional `token` is sent as `Authorization: Bearer <token>` on every request
to that miner. This is for miners behind a monitoring security contract, such as
the Mac metal miner, which returns `401` without it. The token stays in
`config.yaml` / `miners.json` and is never returned by the admin API or shown in
the UI; editing a miner on the settings page preserves its token rather than
wiping it. Because the current miner exporter is plain HTTP, keep such a miner on
a trusted management network and treat the token as visible to other local users
who can read process arguments. `warn_temp_c` / `crit_temp_c` are optional and default to
64 / 70 °C. Both AxeOS variants trip their own thermal protection at 70 °C, so
red sits on that line and amber gives a few degrees of warning first.

### bitcoin

```yaml
bitcoin:
  provider: public            # public | core | demo
  base_url: https://mempool.space
  interval: 30s
  timeout: 8s
```

`public` uses mempool.space (or a self-hosted mempool instance if you change
`base_url`). `demo` simulates it. `core` (direct Bitcoin Core RPC) is reserved;
see [Optional extensions](#optional-extensions).

### pool

Pool statistics are provider-agnostic. Each miner is routed to a provider by its
stratum host (when `provider: auto`) or by an explicit override, and the results
are merged into one panel. Fields a provider cannot supply show as unavailable
(an em-dash), never as a fake zero.

```yaml
pool:
  provider: auto              # default for miners without their own override
  base_url: https://public-pool.io:40557   # applies to Public Pool
  token: ""                   # Braiins API token only; stays out of the UI
  interval: 60s
  timeout: 8s
```

`provider` values:

- `auto` detects the provider per miner from its stratum host, and falls back to
  `generic` for anything unrecognised. Best for a mixed fleet.
- `publicpool`, `ckpool`, `braiins`, `btcpowlab` force that provider for all miners without
  their own override.
- `generic` derives stats from each miner's own telemetry (hashrate, shares,
  best share) with no external API. This is also the automatic fallback for an
  unknown pool or a Braiins miner with no token.
- `none` hides the pool panel; `demo` simulates it.

Per miner, `pool_provider` overrides the default (see the miners section).
`base_url` applies to Public Pool; ckpool, Braiins and BTC PoW Lab use their own defaults.
`token` is required only for Braiins; it is read from this file and never sent to
the admin API or the browser.

Capability discovery means a missing metric is normal: for example ckpool
reports accepted shares but not rejected ones, so the rejected figure falls back
to the miners' own counts and is labelled as such.

### dashboard

```yaml
dashboard:
  bind: 0.0.0.0               # 127.0.0.1 to keep it off the LAN
  port: 8080
  admin_bind: 127.0.0.1
  settings: true              # false disables /settings and /healthz entirely
  settings_path: /var/lib/rmm/thresholds.json
  screensaver_minutes: 15     # 0 disables the burn-in screensaver
  metrics: true               # expose GET /metrics (Prometheus), LAN-gated
```

`metrics: true` serves Prometheus metrics at `/metrics`, gated to the local
network like the other operator surfaces. Scrape it from Prometheus/Grafana on
the same LAN; set `metrics: false` to remove the route.

`settings: false` removes the admin surface completely — use it if the service
is ever reachable from outside the LAN. `settings_path` also determines where
`miners.json` and `history.gob` live (same directory). See
[SECURITY.md](SECURITY.md) for why the admin surface must not sit behind a
reverse proxy.

### alerts

```yaml
alerts:
  webhook_url: ""          # opt-in; empty disables all alerts
  offline_minutes: 10      # alert when a miner is offline this long (0 = off)
  temp_alerts: true        # alert when ASIC temperature reaches its critical level
  cooldown_minutes: 30     # minimum gap between repeats of the same alert
```

Opt-in operator notifications. Nothing runs unless `webhook_url` is set. When it
is, each alert POSTs a small JSON body (`{miner, kind, level, message, value,
source}`) to that URL, which works with ntfy or a Slack/Discord webhook wrapper.
Alerts are edge-triggered with the cooldown, and a condition that clears resets
so a genuine recurrence is not swallowed. The critical temperature comes from
each miner's own threshold (see the settings page). The URL is a secret held in
the config only, never exposed by the API or UI, and delivery is outbound only
to exactly that URL. For richer alerting, scrape `/metrics` with Prometheus and
use Alertmanager instead.

### history

```yaml
history:
  enabled: true
  retention_days: 7
```

Rolling fleet totals kept in RAM and persisted to `history.gob`. Per-miner
history (hashrate, power, ASIC temperature) is tracked for up to 16 miners and
shown on the history page's per-miner selector; a miner tile on the dashboard
links to its detail charts.

## Runtime settings (the `/settings` page)

Reachable from the local network only. No restart is needed for any of these:

- **Miners** — add, edit or remove miners and set their poll interval. A newly
  added AxeOS miner is polled at once and shows offline until it answers.
- **Data providers** — the Bitcoin and pool base URLs.
- **Appearance** — the animated mark per miner and for the fleet total.
- **Screensaver** — off / floating panel / blank, and the idle timeout.
- **Monitoring** — switch a miner off to hide it and stop polling it (the miner
  keeps mining; only the monitor stops looking).
- **Temperature thresholds** — per-miner amber/red levels, or the fleet default.

## Command-line flags

```
--config <path>     path to config.yaml (default: config.yaml)
--demo              simulated miners, pool and network; ignores config.yaml
--addr host:port   override the listen address
--log-level        debug | info | warn | error
--version          print the version and exit
```

## Versioning

The version shown in the header (`v0.8.2`) comes from `var version` in
`cmd/rmm/main.go`, stamped into the build by the Makefile. It is bumped on every
change: the patch digit for fixes, the minor digit for features. `GET
/api/v1/version` returns it; a `404` there means an old binary is still running.

## Optional extensions

Interfaces exist for these but they are not built yet:

- **`bitcoin: core`** — read network data straight from your own Bitcoin Core
  node over RPC instead of mempool.space. Fully local, no third-party API.
- Per-miner history and detail charts, and a Prometheus metrics endpoint.

The pool integration is now provider-agnostic and ships adapters for Public
Pool, ckpool, Braiins and a generic telemetry fallback (see the pool section).

See the main open-items list for status.
