# Pelt

A fast HTTP load tester and benchmark CLI in Go. One static binary, standard
library only.

- **Fast:** about 54,600 req/s from one process on a 4-vCPU machine.
- **Clear results:** latency percentiles (p50 to p99), a histogram, status
  codes, errors by kind, and TCP connections opened vs reused.
- **Flexible runs:** fixed count (`-n`), duration (`-d`), concurrency (`-c`)
  and a rate cap (`-rate`); any method, headers and body; HTTP/2 with `-http2`.
- **CI ready:** pass/fail thresholds such as `-threshold 'p95<300ms'` fail
  the job with exit code 3; JSON output (`-o json`); Ctrl-C still prints a
  report.
- **User flows:** `pelt run shop.json` runs virtual users through scripted
  journeys (log in, browse, check out) with cookies, captured values and a
  report per step.

## Install

```sh
# Linux and macOS
curl -fsSL https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.sh | sh
```

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.ps1 | iex
```

```sh
# With Go 1.23+
go install github.com/namnguyen3019/pelt/cmd/pelt@latest
```

Versions, prebuilt binaries and building from source: [docs/install.md](docs/install.md).

## Quick start

```text
$ pelt -n 50000 -c 1000 http://127.0.0.1:8080/
Requests:     50000 completed, 50000 responses, 0 errors (0.00%)
Throughput:   54598.3 requests/sec, 3.3 MiB/sec read
Connections:  632 opened, 49368 reused

Latency (successful requests):
  p50     12.215 ms
  p90     24.321 ms
  p99     84.674 ms
...
```

Only point it at servers you own or have permission to test. All flags,
examples, the full output and how to read it: [docs/usage.md](docs/usage.md).

To fail a CI job when the service misses its targets, add thresholds. The run
exits 3 if any is missed:

```sh
pelt -d 30s -c 100 -threshold 'p95<300ms' -threshold 'errors<1%' https://staging.example.com/
```

To see where a service starts to struggle, ramp the load up and down. Each
second's rate, latency and errors land in a Timeline, in JSON and, with
`-series out.csv`, in a CSV file:

```sh
pelt -stages 30s:200,2m:200,30s:0 -series out.csv https://staging.example.com/
```

More: [load stages](docs/usage.md#load-stages) and
[per-second results](docs/usage.md#per-second-results).

Metrics and output: [docs/usage.md#thresholds](docs/usage.md#thresholds). Flow
files take per-flow and per-step targets too: [docs/flows.md#thresholds](docs/flows.md#thresholds).

For multi-step journeys, describe them in a JSON file and run it:

```sh
pelt run examples/shop.json -u 50 -d 1m
pelt run examples/shop.json -stages 1m:100,5m:100,1m:0   # ramp users up, hold, ramp down
```

Each virtual user logs in once, keeps its own cookies, and loops through
weighted flows, carrying values such as a token or an order id from one
request to the next. The file format, captures, [load stages](docs/flows.md#load-stages)
and report: [docs/flows.md](docs/flows.md).

## Pelt vs Locust

| | Pelt | Locust |
|---|---|---|
| Install | One binary | `pip install locust` |
| Test defined by | CLI flags for one endpoint, or a JSON file of user flows | Python code, user journeys |
| Interface | Terminal, text or JSON | Web UI, or headless |
| Scale out | One process, all cores | Master/worker across machines |

Use Pelt to hammer one endpoint fast, or to run straightforward user flows
from a JSON file, from a terminal or CI; use Locust when a journey needs real
code (branches, loops, libraries) or a web UI. More in
[docs/comparison.md](docs/comparison.md).

## More

- **AI agents:** an [Agent Skill](.agents/skills/pelt/SKILL.md) for Devin and
  Claude Code; copy the folder into your project
  ([how](docs/usage.md#use-pelt-from-ai-agents)).
- **How it works:** worker pool, transport tuning, tests:
  [docs/design.md](docs/design.md).
