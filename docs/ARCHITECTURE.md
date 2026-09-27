# Architecture

Raspberry Mining Monitor is a read-only dashboard for AxeOS-compatible solo
miners. It polls each miner, a Bitcoin data provider and a solo pool, folds the
results into one document, and pushes that document to the browser once a
second. The Pi it runs on is never in the mining path.

## Shape

One Go binary. The frontend (hand-written HTML/CSS/JS) is embedded with
`go:embed`, so a single ARM64 file of a few megabytes carries everything. There
is no database, no runtime dependency to install, and no external asset: the
strict Content-Security-Policy forbids every off-origin request, so the page
loads even with no internet.

```
 miners ─┐
 pool  ──┼─▶ collectors ─▶ state store ─▶ dashboard.Build ─▶ HTTP (SSE + REST)
 bitcoin ┘        │              │                                   │
                  └─ history ◀───┘                                browser
```

Data flows one way. Collectors write to the state store; the store fans out to
subscribers; the HTTP layer projects the store into a browser document. Nothing
downstream can reach back to a miner.

## Packages (`internal/`)

- **`config`** — loads and validates `config.yaml`, applies defaults, and builds
  the demo fleet for `--demo`. Distinguishes an omitted field from an explicit
  zero, so `port: 0` or `screensaver: 0` mean what they say.
- **`model`** — the `Source` freshness primitive (last success, last error, age)
  embedded by every snapshot type.
- **`miner`**, **`miner/axeos`** — the miner interface and the read-only AxeOS
  collector. It detects the firmware variant once (upstream Bitaxe's flat
  `/api/system/info` in mV/mA versus NerdQAxe's nested `/api/v2/dashboard` in
  V/A) and normalises both to TH/s, volts and amps. GET only; there is no write
  path to a miner anywhere in the codebase.
- **`pool`**, **`pool/publicpool`**, **`pool/btcpowlab`** — the provider router
  and pool clients. Public Pool and BTC PoW Lab are queried once per payout
  address, with hashrate converted from H/s to TH/s.
- **`bitcoin`**, **`bitcoin/mempool`** — the provider interface and the
  mempool.space client (tip height, network hashrate, difficulty retarget,
  BTC/EUR price). The block subsidy is computed locally from height, not
  fetched.
- **`subsidy`**, **`probability`**, **`aggregate`** — pure maths, unit-tested.
  Probability uses `math.Expm1` for numerical stability at long odds.
- **`demo`** — deterministic, seeded simulators for miners, pool and network, so
  `--demo` produces a lifelike dashboard with no hardware.
- **`state`** — the in-memory snapshot store. Thread-safe, returns copies, fans
  out change notifications to SSE subscribers without letting a slow browser
  stall the collectors, and reconciles the miner set when it changes at runtime.
- **`settings`** — persisted display preferences: per-miner temperature
  thresholds, monitoring on/off, per-miner icon, and screensaver mode/minutes.
  Atomic writes, mode 0600.
- **`minercfg`** — the editable miner list and provider URLs, seeded from
  `config.yaml` on first run and then owned by the admin page.
- **`collect`** — the poll loops with backoff, and a `Manager` that rebuilds the
  collectors when the miner/provider config changes, with no restart.
- **`history`** — three in-RAM ring buffers (1h at 10s, 24h at 60s, 7d at 600s),
  persisted to a small gob file. No database.
- **`dashboard`** — the allowlist projection. `Build` assembles the browser
  document field by field and masks the payout address here; it never re-serves
  an upstream response, so a future firmware change cannot leak an unexpected
  field to the page.
- **`httpapi`** — routes, Server-Sent Events, the REST snapshot, the static
  frontend, and the security headers. The admin surface is gated to the local
  network (see [SECURITY.md](SECURITY.md)).

`cmd/rmm/main.go` wires it together: load config, seed the miner store, build
the collect manager, start history recording, and serve HTTP. `web/` is the
`go:embed` of `web/dist`.

## HTTP surface

Public (any LAN client):

- `GET /` and the static assets — the dashboard, config and history pages.
- `GET /api/v1/snapshot` — the current document as JSON.
- `GET /api/v1/stream` — Server-Sent Events, one snapshot per second.
- `GET /api/v1/history?range=1h|24h|7d` — the rolling fleet totals.
- `GET /api/v1/version` — the build version, shown in the header.
- `GET /api/v1/probability?ths=<TH/s>` — solo-block odds for the current or a
  hypothetical hashrate (the history page's "what if" calculator).
- `GET /metrics` — Prometheus text (LAN-gated; see SECURITY.md).

Operator only (local network, and only when `dashboard.settings` is on):

- `GET /settings` and `GET/PUT/DELETE /api/v1/settings…` — display preferences.
- `PUT /api/v1/miners`, `PUT /api/v1/providers` — the editable config.
- `GET /healthz` — per-source freshness (loopback only).

## Why these choices

- **One binary, embedded frontend** — deployment is a single `scp`; there is
  nothing to keep in sync and no package manager on the Pi.
- **No database** — the only durable state is a few small files (thresholds,
  miners, history). A corrupt or missing file logs a warning and the service
  keeps serving.
- **Projection, not proxy** — the browser only ever sees fields the dashboard
  package chose to expose, which keeps the payout address masked and the attack
  surface small.
- **Push over poll** — one SSE stream at 1 Hz is cheaper than every tab polling,
  and the browser shows fresh data without a reload.
