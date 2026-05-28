# Lumos

> Fast, lightweight mobile performance benchmarking for Android & iOS.
> Inspired by [Flashlight](https://flashlight.dev/).

[![CI](https://github.com/dsetiawan230294/lumos/actions/workflows/ci.yml/badge.svg)](https://github.com/dsetiawan230294/lumos/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Lumos measures real-device performance — FPS, frame time, CPU, RAM, jank, battery (level + temperature), startup — and:

- **Parallel multi-device execution** with a **work-stealing scheduler** (one slow device never blocks the others).
- **Two dispatch modes**: `distribute` (1 scenario → 1 device, scales throughput) or `replicate` (every scenario on every device, for cross-device comparison).
- **Setup hooks** (`hook: true`) and **timebox** (`timebox: 4m`) for one-shot setup scripts and minimum-duration measured phases.
- **Automatic duplicate-transport dedup** — phones reachable via USB + Wi-Fi + mDNS pairing show up once (best transport wins).
- **Python automation** bridge for scripted scenarios (Appium, uiautomator2, XCUITest, plain `adb`, anything).
- **Manual / interactive mode** (`lumos watch`) — drive the app by hand, capture metrics live with hotkeys.
- **Per-thread CPU breakdown** (Android, on by default), **Perfetto traces** (`--trace`), **per-scenario HTML reports** (`--per-scenario`).
- **Single static binary** for **macOS** (Android + iOS) and **Linux / Windows** (Android only).
- **CI-friendly**: machine-readable JSON, HTML report, perf budgets, regression compare with exit codes 0/1/2 = pass/regression/error.

## Install

### Option 1 — Prebuilt binary (recommended)

Download the static binary for your OS/arch from the [Releases page](https://github.com/dsetiawan230294/lumos/releases/latest):

| OS      | Arch          | Asset                                  |
| ------- | ------------- | -------------------------------------- |
| macOS   | Apple Silicon | `lumos_<version>_darwin_arm64`         |
| macOS   | Intel         | `lumos_<version>_darwin_amd64`         |
| Linux   | x86_64        | `lumos_<version>_linux_amd64`          |
| Linux   | arm64         | `lumos_<version>_linux_arm64`          |
| Windows | x86_64        | `lumos_<version>_windows_amd64.exe`    |

```bash
# macOS / Linux example
curl -fsSL -o lumos \
  https://github.com/dsetiawan230294/lumos/releases/latest/download/lumos_0.1.0_$(uname -s | tr A-Z a-z)_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
chmod +x lumos && sudo mv lumos /usr/local/bin/
lumos --version
```

Checksums are published alongside the binaries as `SHA256SUMS.txt`.

### Option 2 — `go install`

Requires Go 1.25+:

```bash
go install github.com/dsetiawan230294/lumos/cmd/lumos@latest
# or pin a release:
go install github.com/dsetiawan230294/lumos/cmd/lumos@v0.1.0
```

### Option 3 — Build from source

```bash
git clone https://github.com/dsetiawan230294/lumos
cd lumos && make build      # → ./bin/lumos
```

### Python helper (optional, for scripted scenarios)

```bash
pip install lumos-performance-test
# with Appium adapter:
pip install "lumos-performance-test[appium]"
```

> `lumos run` also vendors the helper for zero-setup runs — `pip` is only needed if you want to author scenarios in your own Python project.

Prerequisites:

- **Android**: `adb` (Android Platform Tools) on PATH; device with USB debugging enabled.
- **iOS** (macOS only): Xcode + Command Line Tools; optionally `idb` for richer control.
- **Python**: 3.8+ for scripted scenarios. Manual mode (`lumos watch`) has no Python dependency.

## Quickstart

### 0 — sanity-check your environment

```bash
lumos doctor
```

```
lumos doctor · darwin/arm64 · go1.25.0

  [OK  ] python3                Python 3.13.0
  [OK  ] adb                    Android Debug Bridge version 1.0.41
  [OK  ] android devices        1 ready (GUJ7EIW85P8DSGHI)
  [OK  ] perfetto on device     present on 1 device(s)
  [OK  ] xcrun                  xcrun version 70.
  [WARN] ios devices            none connected
         ↳ plug in an iPhone/iPad, trust the host, or run `xcrun simctl boot <udid>`

5 ok · 1 warn · 0 fail · 0 skip
ready, but some optional features may be unavailable.
```

Each FAIL line carries an actionable hint. Exits 1 if any required dependency is missing.

### 1 — list devices

```bash
lumos devices
```

```
PLATFORM  ID                MODEL          OS
android   GUJ7EIW85P8DSGHI  2310FPCA4G     14
ios       00008120-0011…    iPhone 15      18.4
```

### 2 — run a scripted scenario

`config.yaml`:

```yaml
app:
  android: com.android.settings
scenarios:
  # Optional setup hook: runs once per device at the start of the whole
  # `lumos run`. NOT sampled, NOT timed, no JSON output. Use for login,
  # granting permissions, seeding data, dismissing first-run dialogs.
  - name: login
    script: ./scenarios/login.py
    hook: true

  - name: settings_scroll
    script: ./scenarios/settings_scroll.py
    iterations: 3        # minimum number of measured iterations
    warmup: 1
    cooldown_sec: 1s
    timebox: 4m          # optional: keep iterating past `iterations`
                         # until total measured time >= 4 minutes
  - name: settings_open_close
    script: ./scenarios/settings_open_close.py
    iterations: 3
parallel:
  mode: distribute     # default — round-robin: each scenario runs on exactly one device.
                       # use 'replicate' to run every scenario on every device.
  max_devices: 0       # 0 = all attached
  work_stealing: true
```

**Hooks** (`hook: true`):

- Run **once per device** at the start of the run, before any benchmark
  scenarios — so every device starts in the same prepared state.
- **Parallel across devices** (each device runs its hooks on its own
  goroutine, so the slowest device sets the floor, not the sum).
  Multiple hooks for the same device still run sequentially in declaration
  order.
- Not sampled, not timed, no JSON output — pure setup.
- Failure on any device aborts the run (a broken setup almost always
  invalidates the benchmark numbers).
- `iterations` / `warmup` / `timebox` / `cooldown_sec` are ignored on a hook.

**Timebox** (`timebox: <duration>`, e.g. `4m`, `90s`, `1h`):

- Sets a **minimum** measured wall-clock duration for the scenario.
- After `iterations` measured passes complete, lumos keeps iterating until
  the elapsed measured time ≥ timebox. Total iterations may exceed
  `iterations`, never go below.
- `timebox: 0` (default) means "stop at exactly `iterations`" — the historical
  behavior.
- Useful when individual iterations are short and you want "at least N
  minutes of samples" regardless of per-iteration speed.

**Dispatch modes** (`parallel.mode`):

| mode | behavior | use when |
|---|---|---|
| `distribute` (default) | scenarios are round-robin assigned across devices; each scenario runs on **one** device. Total wall time scales with device count. | you want throughput — "1 test script = 1 test suite on 1 device" |
| `replicate` | every scenario runs on **every** device. | you want a cross-device comparison report |

```bash
lumos run config.yaml -o results/

# restrict to specific devices, or add a per-job timeout
lumos run config.yaml -d GUJ7EIW85P8DSGHI -d emulator-5554 --job-timeout 5m -o results/

# log per-collector sampler errors to stderr (great for diagnosing missing metrics)
lumos run config.yaml --debug -o results/
```

Each iteration produces one JSON file per device with raw samples + summary stats (mean / p50 / p90 / p99 / min / max / std) for FPS, frame_ms, cpu_pct, ram_mb, jank_pct, battery_pct, battery_temp_c.

### 3 — render an HTML report

```bash
lumos report results/
open results/report.html

# also write one HTML per scenario (report_<scenario>.html) — handy when
# you used parallel.mode: distribute and want each scenario standalone.
lumos report results/ --per-scenario
```

Per-device rows with sparklines + aggregate-across-devices row per scenario.

### 4 — compare against a baseline

```bash
lumos compare baseline.json current.json --threshold 5%
echo $?     # 0 = pass, 1 = regression, 2 = error
```

`compare` understands metric direction: FPS regressing means a drop; CPU/RAM/jank/frame_ms regressing means an increase.

```bash
# CI-friendly JSON for downstream tooling
lumos compare baseline.json current.json --threshold 5% --json

# Don't fail the process on regression (still prints the diff):
lumos compare baseline.json current.json --strict=false
```

### 5 — interactive / manual mode

```bash
lumos watch --android com.android.settings -o results/
```

```
Lumos watch · 12.3s · q quit · s start · e end · m mark · r reset · Tab focus
┌─ android · GUJ7EIW85P8DSGHI · com.android.settings  [sampling] ─────────┐
│ FPS  118.3  ▂▃▅▇█▇▆▅▅▇█                                                 │
│ CPU%  34.1  ▂▃▃▄▅▆▅▄▃▃▃                                                 │
│ RAM   92.4  ▁▁▂▂▃▃▃▃▃▃▃                                                 │
│ JNK%   3.1  ▁▁▁▂▁▁▁▁▁▁▁                                                 │
└───── segment: scroll (4.2s) ────────────────────────────────────────────┘
```

Hotkeys:

| key | action |
|---|---|
| `s` / `e` | start / end a named segment (recorded in the JSON output) |
| `m` | drop a point marker on the timeline |
| `r` | reset the focused pane's samples and markers |
| Tab / Shift-Tab | cycle focus across device panes |
| `q` / Ctrl-C | quit and save one JSON file per device |

Useful flags:

- `--no-raw` — disable the TUI (sampler still runs); good for CI / piped output.
- `--duration 30s` — auto-exit after N seconds.

## Writing scenarios (Python)

```python
# scenarios/settings_scroll.py
from lumos import Device, log, mark_start, mark_end

def setup(device: Device) -> None:
    log("info", f"setup on {device.id}")

def run(device: Device, iteration: int) -> None:
    mark_start("launch")
    device.shell("am", "start", "-W", "com.android.settings/.Settings")
    mark_end("launch")

    mark_start("scroll")
    for _ in range(6):
        device.shell("input", "swipe", "500", "1500", "500", "500", "200")
    mark_end("scroll")

def teardown(device: Device) -> None:
    device.shell("am", "force-stop", "com.android.settings")
```

The scenario runs in a **separate Python process** per iteration. The host (Go) streams metrics in parallel and ties markers from `mark_start` / `mark_end` / `mark` calls into the JSON output via line-delimited JSON-RPC over stdout.

Lumos ships a vendored copy of the `lumos` Python helper, so scenarios run with the system Python — no `pip install` required. If you prefer a pinned version: `pip install lumos-py`.

See [scenarios/examples/](scenarios/examples/) for runnable scripts (incl. an Appium example).

### Appium (first-class adapter)

For Appium scenarios, Lumos can **auto-create the WebDriver** from your YAML
config so test files stay pure test logic — no driver wiring boilerplate:

```yaml
# config.yaml
app:
  android: com.example.app
  appium:
    server_url: http://localhost:4723/wd/hub   # optional; falls back to LUMOS_APPIUM_URL
    activity: com.example.MainActivity         # appActivity (Android)
    auto_launch: false                         # skip Appium's session-start am start
    no_reset: true                             # preserve app state across sessions
    caps:                                      # arbitrary extra W3C caps
      newCommandTimeout: 300
```

When `app.appium` is present, the harness opens the Appium session before
calling each `run()` and passes the driver in as an extra argument:

```python
from lumos import Device

def run(device: Device, driver, iteration: int) -> None:
    # driver is a connected appium.webdriver.Remote — pure test logic from here.
    driver.find_element("id", "user").send_keys("demo")
    driver.find_element("id", "pw").send_keys("secret")
    driver.find_element("id", "go").click()
```

Setup hooks (`hook: true`) receive the same auto-injected driver, so a
typical pattern is: hook handles login once per device → scenarios always
start from "logged in".

If you'd rather keep `app.appium` unset and create the driver manually
inside the scenario, the `lumos.appium` helpers still work:

```python
from lumos import Device
from lumos.appium import session, traced

def run(device: Device, iteration: int) -> None:
    with session(device, caps={"appActivity": "..."}) as driver:
        with traced("login"):
            driver.find_element("id", "user").send_keys("demo")
            driver.find_element("id", "go").click()
        with traced("scroll_feed"):
            for _ in range(6):
                driver.swipe(500, 1500, 500, 500, 200)
```

`session(device)` picks `UiAutomator2Options` (Android) or `XCUITestOptions`
(iOS) from `device.platform`, wires `udid` / `app_package` / `bundle_id`
from the Lumos env, and uses `$LUMOS_APPIUM_URL` (default
`http://localhost:4723`). Pass `caps={...}` for extras, or `auto_trace=True`
to get a wrapped driver that auto-emits markers for tap / swipe /
find_element calls.

## Custom collectors (plugin API)

Any executable that prints newline-delimited JSON to stdout can plug in as a
collector. One line = one sample:

```json
{"metrics": {"gpu_temp_c": 47.5, "net_rx_kbps": 120}}
{"t": "2026-05-25T10:00:01Z", "metrics": {"gpu_temp_c": 48.0}}
```

Known keys (`fps`, `cpu_pct`, `ram_mb`, `jank_pct`, `battery_pct`, …) are
promoted onto the typed `Sample` fields and flow through summaries + compare
automatically. Anything else lands in `sample.extra.<key>` and is summarised
under the `extra.<key>` namespace in the JSON report.

The plugin receives `LUMOS_DEVICE_ID` in its env and the device ID substituted
for any literal `{device_id}` token in its args. Subprocess-isolated: a
crashing plugin never crashes lumos; stderr is captured into the run logs.

Go API in [`internal/collector`](internal/collector/plugin.go).

## CSV export

Pipe run results into pandas, sheets, or any BI tool:

```bash
# raw timeseries — one row per sample
lumos export results/ -o samples.csv

# per-metric summary — one row per (run, metric) with count/mean/p50/p90/p99/min/max/std
lumos export results/ --mode summary -o summary.csv

# include plugin-supplied extra.* columns alongside built-in metrics
lumos export results/ --mode samples --include-extra
```

Output is deterministic (sorted by scenario / device / iteration), zero-valued
cells are left blank to match the JSON report's `omitempty` semantics, and
timestamps are ISO-8601 UTC.

## Perf budgets (CI gating)

Assert absolute per-metric targets without needing a baseline artifact —
complements `lumos compare` (relative deltas).

```yaml
# budget.yaml
default:
  fps:      { p90: ">= 55", mean: ">= 58" }
  frame_ms: { p90: "<= 18" }
  cpu_pct:  { mean: "<= 30" }
scenarios:
  scroll_feed:
    fps: { p90: ">= 58" }       # tighter than default
```

```bash
lumos check results/ --budget budget.yaml
# Exit 0 = pass, 1 = budget violation(s), 2 = other error.
# --json for machine-readable output.
```

Operators: `<=`, `>=`, `<`, `>`, `==`. Stats: `mean`, `p50`, `p90`, `p99`,
`min`, `max`. Per-scenario rules override the default for the same
`(metric, stat)` pair; other defaults still apply. Metrics missing from a
run are silently skipped, so the same budget file works across scenarios
that emit different metrics.

## Perfetto traces (Android)

Pass `--trace` to capture a Perfetto trace alongside the metrics:

```bash
lumos run config.yaml --trace -o results/
```

For each **measured** iteration (warmup iterations are skipped) lumos starts a
detached perfetto session on the device with a sensible default config
(scheduling, frame timeline, gfx/view/input/am atrace categories,
surfaceflinger framelifecycle, gpu.memory), pulls the resulting
`.perfetto-trace` file into the results dir, and records it as an artifact in
the run JSON:

```json
"artifacts": [
  {"kind": "perfetto", "path": "scroll_001_DEV.perfetto-trace", "size_bytes": 4069505}
]
```

Open the file at <https://ui.perfetto.dev> to drill into a slow frame. Requires
Android 9+ (the `perfetto` binary ships with the OS). Trace start failures are
non-fatal — the run continues without a trace and the reason is logged.

## Per-thread CPU breakdown (Android)

A per-thread CPU% breakdown is captured **by default** on Android, mirroring
Flashlight's Threads view. To disable it (e.g. to shave one adb roundtrip
per sample on very slow links):

```bash
lumos run config.yaml --threads=false -o results/
```

Each tick lumos walks `/proc/<pid>/task/*/stat` in a single adb shell call,
parses every thread's `comm` + `utime+stime`, and emits a per-thread CPU%
map (threads sharing a `comm` — e.g. multiple `GLThread`s — are summed):

```json
"samples": [
  {
    "t": "2026-05-25T12:00:00Z",
    "cpu_pct": 57.7,
    "threads": {
      "ndroid.settings": 29.5,
      "RenderThread":     6.3,
      "binder:2643_3":    5.6,
      "AsyncTask #1":     0.6
    }
  }
]
```

The HTML report aggregates these across every sample of every iteration and
renders a collapsible **Threads** panel per device, sorted desc by mean CPU%
with `mean / max / sample count` and an inline bar normalized to the hottest
thread. JSON schema is additive (`threads` is omitted when the flag is off);
CSV export and budgets are unchanged. iOS isn't supported yet (xctrace doesn't
expose per-thread CPU% cleanly); the `threads` field stays empty for iOS runs.

## Historical trends dashboard

`lumos report` renders a single results dir. `lumos trends` renders **many** —
ideal for tracking CI artifacts over weeks or release cycles:

```bash
lumos trends results/2026-05-{20,21,22,23,24} -o trends.html
```

It walks each directory recursively, groups runs by `(scenario, device,
platform)`, sorts chronologically, and renders a single static HTML page with
SVG sparklines per metric (no JS, no external assets). Each metric row shows
first / last / min / max / Δ% / verdict (improved · regressed · flat) coloured
by the same direction table as `lumos compare`. Warmup iterations are
excluded.

## Emulators & simulators

Android emulators (`emulator-5554`, `emulator-5556`, …) are first-class — they
appear in `adb devices` and lumos treats them like any other device. iOS
**simulators** are deliberately skipped by `lumos run` because xctrace's
real-time sampling doesn't apply; use a physical device or wire up an Appium
scenario.

## Duplicate transports (USB + Wi-Fi + mDNS pairing)

A single phone can appear in `adb devices` multiple times — for example once
as a USB serial, once as `192.168.x.y:43665` over Wi-Fi, and once as
`adb-<serial>-XXXX._adb-tls-connect._tcp.` from mDNS pairing. Running
samplers against the same physical device twice causes them to fight
(each force-stopping the app the other is watching) and produces zeroed
metrics on one side.

Lumos automatically deduplicates by probing `getprop ro.serialno` on each
listed transport and keeping the best one, preferring:

1. **USB serial** (most reliable, lowest latency)
2. `IP:port` (Wi-Fi ADB)
3. mDNS `_adb-tls-connect._tcp.` (kept for pairing; never used for sampling)

So you can leave a phone paired over mDNS *and* connected via USB — it shows
up once in `lumos devices` and gets sampled exactly once.

## CI example (GitHub Actions)

```yaml
- run: lumos run config.yaml -o results/
- run: lumos report results/
- uses: actions/upload-artifact@v4
  with: { name: lumos-report, path: results/ }
- run: lumos compare baselines/main.json results/scroll_001_${{ env.DEVICE }}.json --threshold 5%
```

The final step exits 1 on regression, failing the workflow.

## Architecture (TL;DR)

```
┌─────────────┐   work-stealing pool   ┌────────────────────────┐
│  lumos CLI  ├──────────────────────► │ per-device sampler     │
│ (cobra)     │                        │  ├── CPU /proc/<pid>   │
└─────┬───────┘                        │  ├── RAM dumpsys/PSS   │
      │                                │  ├── FPS gfxinfo       │
      │ stdio JSON-RPC                 │  └── iOS xctrace XML   │
      ▼                                └────────────┬───────────┘
┌─────────────┐                                     │
│ Python      │ ◄─── markers (mark_start/end) ──────┘
│ scenario    │
└─────────────┘
```

- All collectors are pure parsers behind injectable `Execer` interfaces → unit-tested without a device.
- One static Go binary; Python is **only** loaded if a scenario needs it.

## Build, test, release

```bash
make build           # ./bin/lumos
make test            # go test -race ./...
make lint            # golangci-lint
make sync-py         # mirror pkg/lumos/python → internal/pyharness/python
```

Cross-compile:

```bash
GOOS=windows GOARCH=amd64 go build -o lumos.exe ./cmd/lumos
GOOS=linux   GOARCH=amd64 go build -o lumos     ./cmd/lumos
```

## License

[MIT](LICENSE)
