---
name: pelt
description: Load test and benchmark an HTTP or HTTPS endpoint with the Pelt CLI. Use when asked to measure requests per second, latency percentiles (p50/p95/p99), error rate or connection reuse of an API or web server, to stress test or smoke test an endpoint under concurrent load, or to compare performance before and after a change. Not for multi-step user journeys or browser testing.
compatibility: Needs the pelt binary on PATH (Linux, macOS or Windows) and network access to the target.
---

# Pelt: HTTP load testing

Pelt sends many concurrent HTTP requests at one URL and reports throughput,
latency percentiles, a latency histogram, status codes, errors grouped by
kind, and TCP connections opened vs reused.

## Safety rules (read first)

- Only load test servers the user owns or is clearly authorized to test:
  localhost, their own staging, their own services. If ownership is unclear,
  ask before running anything.
- Never point Pelt at third-party production services or public websites
  (APIs you don't run, CDNs, search engines, and so on). That can be a
  denial-of-service attack and can break terms of service.
- Get the user's explicit go-ahead before load testing any production system,
  even their own.
- Start small and ramp up: a smoke test first, then raise `-c` and `-rate`
  step by step while watching errors and latency. Stop when errors appear or
  latency climbs sharply.
- Prefer a time or request cap on every run (`-n` or `-d`). Never start an
  unbounded run.
- Requests with side effects (POST, PUT, DELETE) repeat thousands of times.
  Only send them to an endpoint where that is safe, such as a test database.

## Install

Check first: `pelt -h`. If it is missing:

```sh
# With Go 1.23+ (use the lowercase module path)
go install github.com/namnguyen3019/pelt/cmd/pelt@latest

# Prebuilt binary, Linux or macOS
curl -fsSL https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.sh | INSTALL_DIR="$HOME/.local/bin" sh
```

```powershell
# Prebuilt binary, Windows (PowerShell, no admin; installs to
# %LOCALAPPDATA%\Programs\pelt and adds it to the user PATH)
irm https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.ps1 | iex
```

The Windows installer also updates PATH for the current session, so `pelt`
works right away there; other open terminals need restarting.

`go install` puts the binary in `$(go env GOPATH)/bin`; add that to PATH if
`pelt` is not found. For a local practice target, also install
`github.com/namnguyen3019/pelt/cmd/testserver@latest`
(`testserver -addr 127.0.0.1:8080 -delay 2ms -jitter 8ms -error-rate 0.01`).

## Usage

```text
pelt [flags] <url>
```

The URL must start with `http://` or `https://`. Flags go before the URL.

| Flag | Default | Meaning |
|------|---------|---------|
| `-n` | 200 | Total requests. `0` = no limit (needs `-d`). |
| `-d` | off | Run for this long, e.g. `30s`. Without an explicit `-n`, runs for the full duration. With both, whichever comes first ends the run. |
| `-c` | 50 | Concurrency: workers, i.e. max requests in flight. Capped at `-n`. |
| `-rate` | 0 | Max requests/sec across all workers. `0` = unlimited. |
| `-m` | GET | HTTP method. |
| `-H` | | Header `'Name: value'`. Repeatable. |
| `-body` | | Request body string. |
| `-body-file` | | Read the request body from a file. Not with `-body`. |
| `-t` | 10s | Per-request timeout, including reading the body. |
| `-o` | text | `text` or `json`. |
| `-http2` | false | Allow HTTP/2 over TLS (default forces HTTP/1.1). |
| `-untuned` | false | Go's stock connection pool, for comparison only. |
| `-quiet` | false | Hide the live progress line. |

## Recipes

Always use `-o json` when you will read the numbers yourself. JSON goes to
stdout; the progress line goes to stderr and only appears on a terminal.

```sh
# 1. Smoke test: is it up, and what does one light run look like?
pelt -n 50 -c 5 -o json http://127.0.0.1:8080/health

# 2. Fixed request count
pelt -n 10000 -c 100 -o json http://127.0.0.1:8080/

# 3. Fixed duration
pelt -d 30s -c 100 -o json http://127.0.0.1:8080/

# 4. Steady rate (latency at a known load, not max throughput).
#    Keep -c well above rate x expected latency in seconds.
pelt -d 30s -c 200 -rate 1000 -o json http://127.0.0.1:8080/

# 5. POST JSON with auth
pelt -n 2000 -c 50 -m POST \
  -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
  -body '{"query":"test"}' -o json http://127.0.0.1:8080/api/search

# 6. Body from a file
pelt -n 1000 -c 20 -m PUT -body-file payload.json -o json http://127.0.0.1:8080/items/1

# 7. Pull out the key numbers
pelt -n 5000 -c 100 -o json http://127.0.0.1:8080/ \
  | jq '{rps: .requests_per_sec, errors, status_codes, p50: .latency_ms.percentiles.p50, p99: .latency_ms.percentiles.p99}'
```

Ramp-up pattern: run recipe 4 at increasing rates (for example 100, 500,
1000, 2000) and stop at the first step where errors appear, 5xx grows, or p99
jumps. The last good step is the capacity estimate.

Never put real secrets on the command line in your reply; read them from an
environment variable as in recipe 5.

## Reading the JSON output

```json
{
  "target": "http://127.0.0.1:8080/",
  "method": "GET",
  "concurrency": 20,
  "requests_configured": 500,
  "interrupted": false,
  "elapsed_ms": 176.7,
  "total": 500,
  "responses": 500,
  "errors": 0,
  "requests_per_sec": 2829.8,
  "bytes_read": 31718,
  "connections_opened": 20,
  "connections_reused": 480,
  "latency_ms": {
    "min": 2.648, "mean": 6.856, "stddev": 2.321, "max": 11.831,
    "percentiles": {"p50": 7.126, "p90": 9.920, "p95": 10.146, "p99": 10.539}
  },
  "histogram": [{"up_to_ms": 3.566, "count": 55}, "... 10 buckets"],
  "status_codes": {"200": 493, "500": 7},
  "error_breakdown": {}
}
```

- `total` = `responses` + `errors`. A **response** is any HTTP reply,
  including 4xx and 5xx. An **error** is a transport failure with no reply.
- `status_codes`: count per HTTP status. Check this for 4xx/5xx; they are
  not in `errors`.
- `error_breakdown`: errors by kind: `timeout`, `connection refused`,
  `connection reset`, `out of local ports`, `too many open files`,
  `dns lookup failed`, `tls error`, or `other: <message>`.
- `requests_per_sec`: completed requests (errors included) / elapsed time.
- `latency_ms`: only successful responses. Omitted when there were none.
  Percentiles are exact (nearest rank over every sample).
- `connections_opened` vs `connections_reused`: with keep-alive working,
  opened is about equal to `concurrency` and reused is about `total` minus
  that.
- `interrupted` / `aborted`: the run was stopped by Ctrl-C or SIGTERM;
  results are partial.
- Optional keys appear only when set: `duration_configured`, `rate_limit`,
  `aborted`.

The text output (`-o text`) shows the same numbers with labels `Requests:`,
`Throughput:`, `Connections:`, `Latency`, `Histogram`, `Status codes:` and
`Errors:`.

## Exit codes

`0` the run finished, `130` interrupted (report still printed), `2` bad flags
or config, `1` other failure (for example an unreadable `-body-file`).

**Exit code 0 does not mean the target is healthy.** A run where every
request was refused or returned 500 still exits 0. Always check `errors`,
`error_breakdown` and `status_codes` before calling a run a success.

## What results suggest a problem

| You see | Likely meaning |
|---------|----------------|
| `connection refused` | Nothing listening at that host and port. Check the URL and that the server is up. |
| `dns lookup failed` | Wrong host name. |
| `timeout` growing with load | Server is saturated, or `-t` is shorter than real response time. |
| `connection reset` | Server or a proxy is closing connections, often from overload or a connection limit. |
| `out of local ports` / `too many open files` | The client machine hit its limits. Lower `-c`, or raise `ulimit -n`. Not a server result. |
| Many 5xx in `status_codes` | Server errors under load. Report the share and where in the ramp it started. |
| 429 in `status_codes` | Rate limiting on the target. Lower `-rate`. |
| `connections_opened` far above `concurrency` | Keep-alive is not working (server closes connections, or `-untuned` was used). Throughput and latency then include connection setup. |
| p99 much higher than p50 | Tail latency: queueing, GC pauses or slow dependencies. |
| Raising `-c` raises latency but not `requests_per_sec` | Target is saturated. Extra concurrency is only queueing (latency ≈ concurrency ÷ throughput). |
| Very high `requests_per_sec` with `errors` equal to `total` | Requests failed fast. The rate is meaningless. |

## Reporting to the user

Give the command you ran, then: requests per second, p50/p95/p99, error rate,
non-2xx status counts, and connections opened vs reused. Say what load you
used (`-c`, `-rate`, `-n` or `-d`). Note that results from the same machine
as the server mostly measure that machine; for a real benchmark run Pelt on a
separate host.

## Limits

- One URL per run, no scripted user flows, cookies or per-request variation.
  For multi-step journeys use a scripted tool such as Locust or k6.
- Closed loop: a worker waits for each response before sending the next, so
  a stalling server gets fewer requests (coordinated omission) and tail
  latency can look better than real users would see. `-rate` with ample `-c`
  reduces this.
- No distributed mode. One process uses all CPU cores on its machine.
