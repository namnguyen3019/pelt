---
name: pelt
description: Load test and benchmark an HTTP or HTTPS endpoint with the Pelt CLI. Use when asked to measure requests per second, latency percentiles (p50/p95/p99), error rate or connection reuse of an API or web server, to stress test or smoke test an endpoint under concurrent load, to compare performance before and after a change, or to load test a multi-step user journey (log in, browse, check out) written as a JSON scenario with `pelt run`. Not for browser testing.
compatibility: Needs the pelt binary on PATH (Linux, macOS or Windows) and network access to the target.
---

# Pelt: HTTP load testing

Pelt sends many concurrent HTTP requests at one URL and reports throughput,
latency percentiles, a latency histogram, status codes, errors grouped by
kind, and TCP connections opened vs reused. With `pelt run <scenario.json>`
it instead runs virtual users through scripted flows and reports per step and
per flow; see [User flows](#user-flows-pelt-run).

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
- Flows usually write data (sign-ups, carts, orders) on every iteration. Run
  them only against a test system, never production, and check them with
  `-dry-run` before any load.
- Never write passwords or tokens into a scenario file; use `{{$env NAME}}`.
  `-dry-run` prints headers and bodies in full, so don't paste its output
  anywhere public.

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
| `-threshold` | | Pass/fail target, e.g. `'p95<300ms'`, `'errors<1%'`. Repeatable. Missed = exit 3. See [Thresholds](#thresholds-passfail). |

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

# 8. Gate CI on targets the user gave: exit 3 if any is missed
pelt -d 30s -c 100 -rate 500 -o json \
  -threshold 'p95<300ms' -threshold 'errors<1%' -threshold 'status:5xx<0.5%' \
  http://127.0.0.1:8080/ > pelt.json
```

Ramp-up pattern: run recipe 4 at increasing rates (for example 100, 500,
1000, 2000) and stop at the first step where errors appear, 5xx grows, or p99
jumps. The last good step is the capacity estimate.

Never put real secrets on the command line in your reply; read them from an
environment variable as in recipe 5.

## Thresholds (pass/fail)

`-threshold 'metric op value'` (repeatable, single-URL mode only) makes the
run exit 3 when a target is missed. Quote each one so the shell leaves `<`
and `>` alone. `-threshold` is on the main branch; if `pelt -h` does not list
it, install from main as in [User flows](#user-flows-pelt-run).

| Metric | Value |
|--------|-------|
| `p50`, `p95`, `p99`, `p99.9` (any `pN`), `mean`, `max` | Latency: `300ms`, `1.5s`, or bare ms |
| `errors` | Transport errors (no HTTP reply): `1%` of requests, or a count like `5` |
| `status:5xx`, `status:4xx`, `status:2xx`, `status:503` | Share of requests (`0.5%`) or a count |
| `rps` | Requests per second, e.g. `rps>500` |
| `requests` | Completed requests, e.g. `requests>=10000` |

Operators: `<`, `<=`, `>`, `>=`. 5xx responses are not `errors`; use
`status:5xx`. A metric with nothing to measure fails as `no samples`. A bad
expression exits 2 before any load is sent.

- Set thresholds only from targets the user stated (an SLO, "p95 under
  200 ms"). Don't invent targets; if none were given, run without thresholds
  and report the numbers.
- When a run exits 3, read `thresholds[]` in the JSON and report each failed
  one with its `actual` against `target`.

## User flows: `pelt run`

Use this when the test is a journey rather than one URL. Check that the
installed version has it: `pelt run -h`. It is on the main branch but not
in a release yet, so if `pelt run` is unknown, install from main:
`go install github.com/namnguyen3019/pelt/cmd/pelt@main`.

```text
pelt run [flags] <scenario.json>
```

A minimal scenario:

```json
{
  "name": "shop",
  "base_url": "http://127.0.0.1:8080",
  "load": { "users": 10, "duration": "30s" },
  "defaults": { "think": "200ms-1s" },
  "data": { "file": "users.csv", "mode": "unique" },
  "setup": [
    { "name": "login", "method": "POST", "url": "/login",
      "json": { "username": "{{username}}", "password": "{{password}}" },
      "check": { "status": 200 },
      "capture": [ { "json": "token", "as": "token" } ] }
  ],
  "flows": [
    { "name": "browse", "weight": 7, "steps": [
      { "name": "list", "url": "/products", "check": { "status": 200 },
        "capture": [ { "json": "items[0].id", "as": "id" } ] },
      { "name": "view", "url": "/products/{{id}}", "check": { "status": 200 } } ] },
    { "name": "checkout", "weight": 3, "steps": [
      { "name": "add", "method": "POST", "url": "/cart",
        "headers": { "Authorization": "Bearer {{token}}" },
        "json": { "product_id": 1, "qty": 1 }, "check": { "status": 200 } },
      { "name": "pay", "method": "POST", "url": "/checkout",
        "headers": { "Authorization": "Bearer {{token}}" },
        "check": { "status": 201 } } ] }
  ]
}
```

- Each user runs `setup` once, then loops: pick a flow by weight, run its
  steps in order, think between steps. Each user has its own cookie jar.
- `{{name}}` reads captures, setup captures, the user's CSV row, then `vars`.
  Built-ins: `{{$vu}}`, `{{$iteration}}`, `{{$uuid}}`, `{{$rand 1 100}}`,
  `{{$env NAME}}`. Unknown names fail at load.
- Captures: `json` (dot path like `items[0].id`), `header`, `regex`, `status`.
- A failed `check.status` or an empty capture aborts the rest of that
  iteration unless the step has `"on_fail": "continue"`. A step with no
  `check` accepts any status, so add checks.
- Unknown keys are errors (exit 2, with line and column).

Flags override the file's `load` block: `-u` users, `-d` duration, `-i`
total iterations, `-t` timeout, `-base` base URL, `-seed`, `-o json`,
`-quiet`, `-http2`, `-dry-run`. They may go before or after the file.

```sh
# 1. Always first: one user, setup and each flow once, every request printed.
#    Exits 1 if any step failed.
pelt run scenario.json -dry-run -base http://127.0.0.1:8080

# 2. Small run, then raise -u step by step
pelt run scenario.json -u 5 -d 30s -o json

# 3. Pull out the key numbers
pelt run scenario.json -u 20 -d 1m -o json \
  | jq '{iterations, failed_checks, errors, status_codes,
         steps: [.steps[] | {flow, name, failed_checks, p95: .latency_ms.percentiles.p95}]}'
```

JSON has `"mode": "flow"`, the same top-level totals as single-URL mode,
plus `iterations` (`started`, `completed`, `aborted`, `cut`, `per_sec`),
`failed_checks`, `setup_failed_users`, `flows[]` (with `aborted_at`,
`flow_time_ms` = request time only, `wall_time_ms` = with think time) and
`steps[]` (per step: `requests`, `errors`, `failed_checks`, `latency_ms`,
`status_codes`). A run is healthy only if `failed_checks`, `errors` and
`setup_failed_users` are 0 and `iterations.aborted` is small; exit code 0 does
not mean that. Exit 3 (`"passed": false`, `fail_reason`
`"no iteration completed"`) means no iteration finished at all. Flow mode
does not take `-threshold` yet. `cut` iterations were stopped by the end of the run, not
failures. Full reference: `docs/flows.md` in the repository.

To practise locally: `testserver -app shop -addr 127.0.0.1:8080` serves a
small shop (`POST /login` for users `user001`..., `GET /products`,
`POST /cart`, `POST /checkout`, `GET /orders/{id}`), and
`examples/shop.json` with `examples/users.csv` is a scenario for it.

## Reading the JSON output (single-URL mode)

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
- `passed`: `false` when a threshold was missed or no request got a
  response; always present. `fail_reason` says `"no request got a response"`
  when that rule failed the run.
- `thresholds` (only with `-threshold`): one object per threshold, failures
  first: `expr`, `metric`, `op`, `target`, `actual` (`null` with
  `no_samples`), `unit` (`ms`, `%`, `count`, `per_sec`), `count` and `of` for
  rates, and `pass`.
- Optional keys appear only when set: `duration_configured`, `rate_limit`,
  `aborted`, `fail_reason`, `thresholds`.

The text output (`-o text`) shows the same numbers with labels `Requests:`,
`Throughput:`, `Connections:`, `Latency`, `Histogram`, `Status codes:` and
`Errors:`, then `Thresholds:` and a `Result: PASS` or `Result: FAIL (...)`
line when there are thresholds or the run failed. With `-o json`, a failing
run also prints `pelt: FAIL: ...` to stderr.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Finished; every threshold passed (or none were set) |
| `1` | Other failure, for example an unreadable `-body-file` |
| `2` | Bad flags, config or threshold; nothing was sent |
| `3` | A threshold was missed, or no request got a response (flow mode: no iteration completed) |
| `130` | Interrupted (report still printed), even if a threshold failed |

**Without thresholds, exit code 0 does not mean the target is healthy.** It
only means at least one request got a response: a run where every request
returned 500, or half of them timed out, still exits 0. Check `errors`,
`error_breakdown` and `status_codes` before calling a run a success, or set
thresholds for the targets the user cares about.

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

- Single-URL mode sends one fixed request; use `pelt run` for journeys,
  cookies and per-request variation.
- Flow mode has no ramp-up, target-rate mode, branches or loops yet, and
  checks only look at the status code. For journeys that need real code, use
  a scripted tool such as Locust or k6.
- Closed loop: a worker waits for each response before sending the next, so
  a stalling server gets fewer requests (coordinated omission) and tail
  latency can look better than real users would see. `-rate` with ample `-c`
  reduces this.
- No distributed mode. One process uses all CPU cores on its machine.
