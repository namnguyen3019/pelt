# Pelt

A small HTTP load testing and benchmarking CLI written in Go, using only the
standard library. It fires thousands of concurrent requests from a pool of
goroutines and reports throughput (requests per second), latency percentiles,
a latency histogram, status codes, errors grouped by kind, and how well
connections were reused.

## Highlights

- **One static binary.** Download it and run it. No Go, Python or other
  runtime needed, and no config file or test script to write first.
- **Fast on one machine.** About 54,600 requests/sec with 1,000 workers in the
  sample below (client and server sharing one 4-vCPU machine), from a single
  process that uses every CPU core.
- **Latency you can trust.** min, mean, stddev, max, p50, p90, p95 and p99,
  plus a histogram, status codes and errors grouped by kind (timeout,
  refused, reset and so on).
- **Connection reuse you can see.** The connection pool is sized to the
  worker count, and the report shows TCP connections opened vs reused. Tuned,
  it ran 48,589 req/s against 26,053 req/s with Go's stock settings
  ([details](#why-the-transport-tuning-matters)).
- **Three ways to size a run.** A fixed request count (`-n`), a fixed duration
  (`-d`), or both, with bounded concurrency (`-c`, tested up to 5,000 workers)
  and an optional global rate limit (`-rate`).
- **Any HTTP request.** Any method, repeatable headers, and an inline or file
  request body. HTTP/1.1 by default, HTTP/2 with `-http2`.
- **Built for scripts and CI.** JSON output (`-o json`), clear exit codes, and
  Ctrl-C stops the run but still prints a report of everything so far.
- **Small and well tested.** Standard library only. Unit and integration tests
  run clean under the race detector.

A run looks like this:

```text
$ pelt -n 50000 -c 1000 http://127.0.0.1:8080/
Target:       GET http://127.0.0.1:8080/
Load:         50000 requests, 1000 workers, no rate limit
Elapsed:      916ms

Requests:     50000 completed, 50000 responses, 0 errors (0.00%)
Throughput:   54598.3 requests/sec, 3.3 MiB/sec read
Connections:  632 opened, 49368 reused

Latency (successful requests):
  min     2.082 ms
  mean    15.119 ms
  stddev  12.387 ms
  max     160.660 ms
  p50     12.215 ms
  p90     24.321 ms
  p95     29.277 ms
  p99     84.674 ms

Histogram:
   17.939 ms  [  38206]  ■■■■■■■■■■■■■■■■■■■■■■■■■■■■■■■■■■■■■■■■
   33.797 ms  [  10202]  ■■■■■■■■■■
   49.655 ms  [    592]  ■
   65.513 ms  [    120]  ■
   81.371 ms  [    242]  ■
   97.229 ms  [    464]  ■
  113.087 ms  [     70]  ■
  128.944 ms  [     81]  ■
  144.802 ms  [     19]  ■
  160.660 ms  [      4]  ■

Status codes:
  200  49758
  500  242
```

## Install

**One-line installer, Linux and macOS** (amd64 and arm64):

```sh
curl -fsSL https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.sh | sh
```

It picks the release archive for your OS and CPU, checks its SHA-256 against
the release's `checksums.txt`, and installs `pelt` to `/usr/local/bin` (using
`sudo` only if that directory is not writable). Options:

```sh
# a specific version
curl -fsSL https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.sh | sh -s -- v0.1.0

# somewhere you own, no sudo
curl -fsSL https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.sh | INSTALL_DIR="$HOME/.local/bin" sh
```

**One-line installer, Windows** (amd64 and arm64), in PowerShell:

```powershell
irm https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.ps1 | iex
```

It downloads `pelt_windows_<arch>.zip`, checks its SHA-256 against
`checksums.txt`, puts `pelt.exe` in `%LOCALAPPDATA%\Programs\pelt` and adds
that folder to your user PATH, so no admin rights are needed. `pelt` works
right away in that window; other open terminals need restarting. To pick a version or folder, set `PELT_VERSION` (e.g.
`v0.1.0`) or `PELT_INSTALL_DIR` first, or set `PELT_NO_MODIFY_PATH=1` to leave
PATH alone:

```powershell
$env:PELT_VERSION = 'v0.1.0'; irm https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.ps1 | iex
```

**Prebuilt binary.** Every [release](https://github.com/namnguyen3019/pelt/releases)
has `pelt_<os>_<arch>.tar.gz` for `linux` and `darwin` (`amd64`, `arm64`),
`pelt_windows_<arch>.zip` for Windows, and a `checksums.txt`. For example:

```sh
curl -fsSLO https://github.com/namnguyen3019/pelt/releases/latest/download/pelt_linux_amd64.tar.gz
tar xzf pelt_linux_amd64.tar.gz pelt
sudo mv pelt /usr/local/bin/
```

**With Go** (1.23 or newer). Use the lowercase module path:

```sh
go install github.com/namnguyen3019/pelt/cmd/pelt@latest
```

This puts a `pelt` binary in `$(go env GOPATH)/bin`. To also get the local
test server:

```sh
go install github.com/namnguyen3019/pelt/cmd/testserver@latest
```

**From source:**

```sh
git clone https://github.com/namnguyen3019/pelt.git
cd pelt
go build -o pelt ./cmd/pelt
sudo mv pelt /usr/local/bin/
```

### Releasing

Pushing a tag that starts with `v` runs
[`.github/workflows/release.yml`](.github/workflows/release.yml), which tests,
builds all six targets and publishes a GitHub Release with the archives and
`checksums.txt`:

```sh
git tag v0.1.0
git push origin v0.1.0
```

## Quick start

From a clone of this repository:

```sh
go build -o bin/ ./cmd/...

# terminal 1: a local target with 2 to 10 ms of latency and 0.5% errors
./bin/testserver -addr 127.0.0.1:8080 -delay 2ms -jitter 8ms -error-rate 0.005

# terminal 2
./bin/pelt -n 10000 -c 200 http://127.0.0.1:8080/
```

Only point it at servers you own or have permission to test.

## Usage

```text
pelt [flags] <url>
```

| Flag | Default | Meaning |
|------|---------|---------|
| `-n` | 200 | Total requests. `0` means no limit (needs `-d`). |
| `-d` | off | Run for this long, e.g. `30s`. Without an explicit `-n`, runs for the full duration. |
| `-c` | 50 | Concurrency: number of worker goroutines, i.e. max requests in flight. |
| `-rate` | 0 | Max requests per second across all workers. `0` is unlimited. |
| `-m` | GET | HTTP method. |
| `-H` | | Header as `'Name: value'`. Repeat for more. A `Host` header sets the Host. |
| `-body` | | Request body. |
| `-body-file` | | Read the request body from a file. |
| `-t` | 10s | Per-request timeout, including reading the response body. |
| `-o` | text | Output format: `text` or `json`. |
| `-http2` | false | Allow HTTP/2 over TLS. By default the tool forces HTTP/1.1. |
| `-untuned` | false | Use Go's stock connection pool limits, to see why tuning matters. |
| `-quiet` | false | Hide the live progress line (only shown on a terminal). |

Examples:

```sh
# 30 seconds at a steady 2,000 req/s
pelt -d 30s -c 200 -rate 2000 http://127.0.0.1:8080/

# POST JSON, machine-readable output
pelt -n 5000 -c 100 -m POST -H 'Content-Type: application/json' \
  -body '{"hello":"world"}' -o json http://127.0.0.1:8080/api | jq .latency_ms
```

Exit codes: `0` finished, `130` interrupted with Ctrl-C (report still printed),
`2` bad flags or config, `1` other errors.

## Use Pelt from AI agents

This repo ships an [Agent Skill](https://agentskills.io) at
[`.agents/skills/pelt/SKILL.md`](.agents/skills/pelt/SKILL.md). It tells a
coding agent when to reach for Pelt, how to install it, the flags, common
recipes, how to read the JSON output, and safety rules (only test servers you
own, start small, ramp up).

Agents load skills from the repository they are working in, so copy the
folder into your own project:

```sh
# Devin (and other tools that read .agents/skills)
mkdir -p .agents/skills
cp -r path/to/pelt/.agents/skills/pelt .agents/skills/

# Claude Code
mkdir -p .claude/skills
cp -r path/to/pelt/.agents/skills/pelt .claude/skills/
```

Devin also reads `.claude/skills`, so if you use both, the `.claude/skills`
copy alone is enough. For Claude Code across all your projects, put it in
`~/.claude/skills/pelt` instead.

## Pelt vs Locust

[Locust](https://docs.locust.io) is a popular open source load testing tool where
you describe user behaviour in Python. The two tools aim at different jobs:

| | Pelt | Locust |
|---|---|---|
| Install | One static binary, or `go install` | `pip install locust` (needs Python) |
| Defining a test | Command-line flags: one URL, method, headers, body | Python code: user classes with tasks, so logins, loops, conditions and any Python library |
| Load model | A fixed pool of workers (`-c`) sending back to back, with an optional global rate cap | Simulated users with wait times, a spawn rate to ramp up, and custom load shapes |
| Interface | Terminal: live progress line, then a text or JSON report | Web UI with live charts and load you can change mid-run; also runs headless |
| Scaling out | One process uses every core; no distributed mode | One process per core (Python's GIL); `--processes` and master/worker mode, across many machines |
| Throughput per process | About 54,600 req/s in the sample above | Up to about 16,000 req/s with `FastHttpUser` or 4,000 with `HttpUser`, best case, per Locust's docs |
| Results | Percentiles, histogram, status codes, error kinds, connections opened vs reused | Live stats and charts, CSV export |
| Protocols | HTTP and HTTPS | HTTP out of the box, any protocol with a custom client |

The throughput figures come from different hardware and different tests (Pelt:
client and server sharing one 4-vCPU machine; Locust: its own docs, one
process on one core), so read them as orders of magnitude, not a benchmark.

**When to use which.** Use Pelt to hammer one endpoint and get throughput,
latency percentiles and connection behaviour in seconds, from a terminal or a
CI job. Use Locust when the test is a user journey (log in, browse, check
out), when you want a web UI to watch and steer the load, or when you need to
spread load across many machines.

## How it works

```text
               jobs (unbuffered chan struct{})
 dispatcher ─────────────────────────────────▶ worker 1 ─▶ recorder 1 ┐
 (when may the                               ▶ worker 2 ─▶ recorder 2 ├─ merge ─▶ stats ─▶ report
  next request start?)                       ▶ worker N ─▶ recorder N ┘
                         shared http.Client (one tuned Transport)
```

**Dispatcher.** One goroutine decides *when* each request may start. It stops
after `-n` jobs, when the `-d` deadline passes, or on Ctrl-C, and then closes
the `jobs` channel. With `-rate`, request *i* is scheduled at
`start + i × interval`. Pacing against an absolute schedule, instead of
sleeping a fixed interval after each send, keeps the long-run rate exact even
when the timer fires late, and lets high rates work despite coarse OS timer
resolution: if the dispatcher falls behind, it catches up with a short burst.

**Worker pool.** `-c` goroutines `range` over `jobs`, so concurrency is
bounded by construction: there can never be more than `-c` requests in
flight. The channel is unbuffered on purpose. A job is handed over only when
a worker is free, so nothing piles up in a queue and the deadline and Ctrl-C
take effect immediately.

**No shared state on the hot path.** Each worker writes into its own
`recorder` (latency slice, status map, error map). No mutex, no results
channel. The recorders are merged once, after `wg.Wait()`. The only shared
writes are a few `atomic.Int64` counters used for the live progress line and
the connection counts.

**Two kinds of stop.** The `-d` deadline only stops *dispatching*; requests
already in flight finish, so the tail of the run is still measured. Ctrl-C
cancels the parent `context.Context`, which stops dispatching *and* aborts
in-flight requests. Aborted requests are counted separately, not as errors,
and the partial report is printed. A second Ctrl-C kills the process, because
`signal.NotifyContext`'s `stop()` restores default signal handling.

```text
$ pelt -d 60s -c 300 http://127.0.0.1:8080/   # Ctrl-C after 2 seconds
Interrupted: partial results below.

Target:       GET http://127.0.0.1:8080/
Load:         for 1m0s, 300 workers, no rate limit
Elapsed:      2.002s

Requests:     84215 completed, 84215 responses, 0 errors (0.00%)
              197 in-flight requests aborted by the interrupt
Throughput:   42060.0 requests/sec, 2.6 MiB/sec read
Connections:  309 opened, 84240 requests reused a connection
...
```

**What latency means here.** The clock starts just before `client.Do` and
stops after the response body has been read to EOF, which is what a real
client experiences. Only requests that got an HTTP response contribute
latency samples. A 500 is a response (it shows under status codes); a
timeout or refused connection is an error.

**Percentiles** use the nearest-rank method on the full, sorted set of
samples, so p99 is always a latency that was actually observed. Keeping every
sample costs 8 bytes per request (8 MB per million), which is cheap for an
exact answer.

### Why the transport tuning matters

Go's `http.DefaultTransport` keeps only **2 idle connections per host**
(`MaxIdleConnsPerHost`). With 500 workers hitting one host, when ~500
responses come back only 2 connections fit in the idle pool and the rest are
closed. Each of those workers then dials a brand-new TCP connection for its
next request. You end up measuring the client's connection churn, and you
leave thousands of sockets in `TIME_WAIT`, which can exhaust local ports.

The tool sizes `MaxIdleConns` and `MaxIdleConnsPerHost` to `-c`, so every
worker keeps one warm keep-alive connection for the whole run. It also always
drains and closes response bodies, because a body that is not read to EOF
cannot go back to the pool. An `httptrace.ClientTrace` counts actual TCP dials
(`ConnectDone`) and reused connections (`GotConn`), so the report shows
whether this is working.

Same machine, same server, 20,000 requests with 500 workers:

| | Tuned (default) | `-untuned` |
|---|---|---|
| Throughput | 48,589 req/s | 26,053 req/s |
| TCP connections opened | 683 | 14,392 |
| Sockets left in TIME_WAIT | 683 | 14,387 |
| p50 latency | 7.58 ms | 12.96 ms |
| p99 latency | 54.84 ms | 99.46 ms |

Other transport choices: compression is disabled so latency is not inflated
by gunzipping on the client, and HTTP/2 is off by default because it would
multiplex all workers over a few connections, which hides how the server
copes with many concurrent connections. Use `-http2` when that is what you
want to test.

At very high concurrency, a few more connections than workers can be opened
(5,965 for 5,000 workers below). Go's transport races a new dial against a
connection coming back to the idle pool; if the pooled connection wins, the
dial still completes and is kept for later.

## Reading the numbers

Two things worth knowing when you interpret a run.

**Concurrency, throughput and latency are linked.** Once the target is
saturated, Little's law says latency ≈ concurrency ÷ throughput. Here the
same server was pushed with 5,000 workers instead of 1,000. Throughput did
not rise; latency did, by almost exactly the predicted amount
(5,000 ÷ 46,738 ≈ 107 ms against a measured mean of 93 ms):

```text
$ pelt -n 100000 -c 5000 http://127.0.0.1:8080/
Target:       GET http://127.0.0.1:8080/
Load:         100000 requests, 5000 workers, no rate limit
Elapsed:      2.14s

Requests:     100000 completed, 100000 responses, 0 errors (0.00%)
Throughput:   46737.7 requests/sec, 2.8 MiB/sec read
Connections:  5965 opened, 98304 requests reused a connection
  ...
  mean    93.484 ms
  p99     289.747 ms
```

So a higher `-c` is not "more load" once the server is the bottleneck; it is
more queueing.

**This is a closed-loop tester.** Each worker waits for a response before
taking the next job. If the server stalls, the workers stall too and stop
sending, so the requests that *would* have arrived during the stall are never
measured. This is known as coordinated omission, and it makes tail latency
look better than users would see. Using `-rate` with enough workers
(`-c` comfortably above rate × expected latency) reduces the effect. An
open-loop design that measures from each request's *scheduled* start time is
a natural next step.

Also note that the sample numbers in this README were taken with the client
and the test server on the same 4-vCPU machine, so they mostly measure that
machine. For a real benchmark, run the tester on a separate host.

## Project layout

```text
cmd/pelt/            CLI: flags, signal handling, progress line, output
cmd/testserver/      local target with configurable delay and error rate
internal/loadtest/   Config, tuned Transport, dispatcher + worker pool, Result
internal/stats/      percentiles, mean/stddev, histogram (pure functions)
internal/report/     text and JSON rendering
```

## Tests

```sh
go test -race ./...
```

- `internal/stats`: nearest-rank percentiles (including the float edge case
  where 99.9% of 1,000 must be the 999th value, not the 1,000th), mean,
  standard deviation, histogram buckets and edges.
- `internal/loadtest` against `httptest.Server`: exact request counts,
  concurrency never exceeding `-c` (measured on the server side), rate limit
  pacing, duration mode not aborting in-flight requests, timeouts and refused
  connections classified correctly, method/headers/body passed through,
  Ctrl-C returning partial results promptly, and tuned vs untuned connection
  reuse.
- `internal/report`: text and JSON output.
- `cmd/pelt`: end-to-end run of the CLI against a local server, and bad
  flag handling.

## Ideas for later

- Open-loop mode that corrects for coordinated omission
- HDR histogram for constant memory on very long runs
- Per-second time series in the JSON output
- Ramp-up of workers over time
