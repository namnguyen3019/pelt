# Pelt

A fast HTTP load tester and benchmark CLI in Go. One static binary, standard
library only.

- **Fast:** about 54,600 req/s from one process on a 4-vCPU machine.
- **Clear results:** latency percentiles (p50 to p99), a histogram, status
  codes, errors by kind, and TCP connections opened vs reused.
- **Flexible runs:** fixed count (`-n`), duration (`-d`), concurrency (`-c`)
  and a rate cap (`-rate`); any method, headers and body; HTTP/2 with `-http2`.
- **Script friendly:** JSON output (`-o json`), exit codes, and Ctrl-C still
  prints a report.

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

## Pelt vs Locust

| | Pelt | Locust |
|---|---|---|
| Install | One binary | `pip install locust` |
| Test defined by | CLI flags, one endpoint | Python code, user journeys |
| Interface | Terminal, text or JSON | Web UI, or headless |
| Scale out | One process, all cores | Master/worker across machines |

Use Pelt to hammer one endpoint fast from a terminal or CI; use Locust for
scripted user flows. More in [docs/comparison.md](docs/comparison.md).

## More

- **AI agents:** an [Agent Skill](.agents/skills/pelt/SKILL.md) for Devin and
  Claude Code; copy the folder into your project
  ([how](docs/usage.md#use-pelt-from-ai-agents)).
- **How it works:** worker pool, transport tuning, tests:
  [docs/design.md](docs/design.md).
