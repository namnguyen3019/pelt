# Using Pelt

[Back to README](../README.md)

This page covers single-URL mode, `pelt [flags] <url>`. For scripted user
journeys with `pelt run <file>`, see [flows.md](flows.md).

## Try it locally

From a clone of this repository:

```sh
go build -o bin/ ./cmd/...

# terminal 1: a local target with 2 to 10 ms of latency and 0.5% errors
./bin/testserver -addr 127.0.0.1:8080 -delay 2ms -jitter 8ms -error-rate 0.005

# terminal 2
./bin/pelt -n 10000 -c 200 http://127.0.0.1:8080/
```

Only point it at servers you own or have permission to test.

## Flags

```text
pelt [flags] <url>
```

| Flag | Default | Meaning |
|------|---------|---------|
| `-n` | 200 | Total requests. `0` means no limit (needs `-d` or stages). |
| `-d` | off | Run for this long, e.g. `30s`. Without an explicit `-n`, runs for the full duration. |
| `-c` | 50 | Concurrency: number of worker goroutines, i.e. max requests in flight. Never more than `-n`. |
| `-rate` | 0 | Max requests per second across all workers. `0` is unlimited. |
| `-stages` | | Change the load over the run, e.g. `30s:200,2m:200,30s:0`. Sets the run length. See [Load stages](#load-stages). |
| `-ramp` | | Ramp concurrency from 0 up to `-c` over this long, then hold for the rest of `-d`. |
| `-m` | GET | HTTP method. |
| `-H` | | Header as `'Name: value'`. Repeat for more. A `Host` header sets the Host. |
| `-body` | | Request body. |
| `-body-file` | | Read the request body from a file. |
| `-t` | 10s | Per-request timeout, including reading the response body. |
| `-o` | text | Output format: `text` or `json`. |
| `-series` | | Also write one row per second of the run to this CSV file. See [Per-second results](#per-second-results). |
| `-http2` | false | Allow HTTP/2 over TLS. By default the tool forces HTTP/1.1. |
| `-untuned` | false | Use Go's stock connection pool limits, to see why tuning matters. |
| `-quiet` | false | Hide the live progress line (only shown on a terminal): requests so far, req/s in the last whole second, errors so far. With stages it shows the stage, its target now and requests in flight instead. |
| `-threshold` | | Pass/fail target such as `'p95<300ms'` or `'errors<1%'`. Repeat for more. A missed target exits 3. See [Thresholds](#thresholds). |

## Examples

```sh
# 30 seconds at a steady 2,000 req/s
pelt -d 30s -c 200 -rate 2000 http://127.0.0.1:8080/

# ramp up to 200 in flight over 30 seconds, hold 2 minutes, ramp down
pelt -stages 30s:200,2m:200,30s:0 http://127.0.0.1:8080/

# POST JSON, machine-readable output
pelt -n 5000 -c 100 -m POST -H 'Content-Type: application/json' \
  -body '{"hello":"world"}' -o json http://127.0.0.1:8080/api | jq .latency_ms
```

## Load stages

By default a run holds one level from start to end. Stages change it over
the run: ramp up, hold, ramp down, or step from one level to another. Use
them to find the load where a service starts to struggle, to warm a cache
before the part you measure, or to see whether it recovers once the load
drops. Flow mode takes stages too, moving the number of users; see
[flow load stages](flows.md#load-stages).

```sh
pelt -stages 30s:200,2m:200,30s:0 https://api.example.com/        # 0 → 200 in flight, hold, → 0
pelt -stages 1m:500/s,5m:500/s -c 300 https://api.example.com/    # 0 → 500 req/s, at most 300 in flight
pelt -stages 0s:50,1m:50,0s:150,1m:150 https://api.example.com/   # 50 for a minute, then 150
pelt -ramp 30s -c 200 -d 3m https://api.example.com/              # same as -stages 30s:200,2m30s:200
```

A stage is `duration:target`. The level moves in a straight line from the
previous stage's target to this one over the duration. The run starts at 0,
so `30s:200` ramps from 0 to 200, and a `0s` stage jumps straight to its
target.

- **A bare number moves concurrency**: requests in flight. Pelt starts as many
  workers as the highest target and sends a request only while fewer than
  the target are in flight. On a ramp-down, requests already in flight finish;
  none is cut. `-rate` still caps the rate.
- **A number with `/s` moves the request rate.** Requests go out on a schedule
  that follows the curve, so `2s:100/s` sends 100 requests in all. `-c`
  caps requests in flight; if the series shows `req/s` below `target`, raise
  it.
- **The stages set the run length.** `-n` still stops the run early, and its
  default of 200 is dropped, as with `-d`.

Each of these exits 2 before any load is sent: `-stages` with `-d` or
`-ramp`; concurrency stages with `-c`; rate stages with `-rate`; a mix of
`100` and `500/s` in one list; a negative target; a stage without a
duration; or stages that add up to 0s. `-ramp` needs `-d` and goes up only;
use `-stages` for a ramp-down.

Thresholds are measured over the whole run, ramps included, exactly as
without stages.

The report gains a Stages block, one row per stage, and the Timeline gains
a `target` column:

```text
Load:         concurrency stages 3s:100,4s:100,3s:0 over 10s
...
Stages:
  stage    time     target  requests   req/s  p95 ms  errors
  1       0s-3s    0 → 100    18,428   6,143  10.411       0
  2       3s-7s  100 → 100    49,563  12,391  10.413       0
  3      7s-10s    100 → 0    18,601   6,200  10.419       0

Timeline (1 second per row):
  time    target   req/s  p95 ms  errors  5xx
  0-1s        17   2,019  10.417       0    0
  1-2s        50   6,155  10.367       0    0
  2-3s        83  10,254  10.444       0    0
  3-4s       100  12,416  10.364       0    0
  ...
```

A second belongs to the stage in effect at its midpoint, and the `target`
shown is the target there, so on a ramp it is the second's average. Requests
that finish after the last stage ends count in the last stage. A `0s` stage
owns no seconds and shows `-`.

In JSON, `stages_configured` holds the list as given and `stages` has one
object per stage:

```json
"stages": [
  {"stage": 1, "name": "", "from": 0, "target": 100, "start_s": 0, "end_s": 3,
   "requests": 18414, "responses": 18414, "errors": 0, "rps": 6138,
   "p50_ms": 8.103, "p95_ms": 10.428, "p99_ms": 10.987}
]
```

The live line shows the stage, its target now, requests in flight, last
second's rate and errors so far:
`stage 2/3  target 100  active 100  12,416 req/s  0 errors`.

## Thresholds

Thresholds turn a run into a pass or a fail, so Pelt can gate a deploy in
CI. Give each target with `-threshold`; the run exits `3` if any is missed.

```sh
pelt -d 30s -c 200 \
  -threshold 'p95<300ms' -threshold 'errors<1%' -threshold 'status:5xx<0.5%' \
  https://staging.example.com/health
```

A threshold is `metric op value`, with `<`, `<=`, `>` or `>=`. Spaces are
allowed; quote it so the shell leaves `<` and `>` alone.

| Metric | Measures | Value |
|--------|----------|-------|
| `p50`, `p95`, `p99`, `p99.9`, any `pN` | Latency percentile of requests that got a response | Duration: `300ms`, `1.5s`, or a bare number of ms |
| `mean`, `max` | Mean or slowest response | Duration |
| `errors` | Transport errors (timeout, refused, reset, DNS, TLS) | Share of requests (`1%`) or a count (`5`) |
| `status:5xx`, `status:4xx`, `status:2xx`, `status:503` | Responses with that status class or code | Share of requests or a count |
| `rps` | Completed requests per second | Number |
| `requests` | Completed requests, to insist the run had enough samples | Count |

`errors` counts only requests that got no HTTP response, the same number as
the report's `errors`. A 500 is a response; target it with `status:5xx`. A
metric with nothing to measure, such as `p95` when no request got a response,
fails with `no samples`.

The text report ends with one line per threshold, failures first, and the
result:

```text
Thresholds:
  FAIL  status:5xx < 0.5%  1.60%  (32 of 2000)
  PASS  p95 < 300ms        10.337 ms
  PASS  errors < 1%        0.00%  (0 of 2000)

Result: FAIL (1 of 3 thresholds missed)
```

With `-o json` the report gains `"passed"` (`true` or `false`, always
present), `"thresholds"` (one object per threshold with `expr`, `metric`,
`op`, `target`, `actual`, `unit`, `count`, `of` and `pass`) and, when the
built-in rule below failed the run, `"fail_reason"`. stdout stays pure JSON;
a failing run also prints one line to stderr, such as
`pelt: FAIL: 1 of 3 thresholds missed`, so it shows in CI logs.

**Without thresholds**, a run passes unless no request got a response at all
(every request refused, timed out or failed DNS). That run exits `3` with
`Result: FAIL (no request got a response)`, because a dead target is never a
pass. Any run with at least one response exits `0`, as before.

In GitHub Actions:

```yaml
- name: Load test staging
  run: |
    pelt -d 60s -c 100 -rate 500 -o json \
      -threshold 'p95<300ms' -threshold 'errors<1%' -threshold 'status:5xx<0.5%' \
      https://staging.example.com/api/health > pelt.json
- uses: actions/upload-artifact@v4
  if: always()
  with:
    name: pelt-report
    path: pelt.json
```

Thresholds are evaluated when the run ends. Flow mode (`pelt run`) takes
the same `-threshold` flag, plus `thresholds` lists in the scenario file per
flow and per step, and metrics for checks and iterations; see
[flow thresholds](flows.md#thresholds).

## Per-second results

Every run also records what happened in each second: how many requests
finished, how they ended, and their latency. A request counts in the second
it **finished**, so the rows add up to the run's totals and a slow request
shows in the second it hurt. Use it to see when a run went wrong, not just
whether it did.

**In the terminal**, a run of 3 seconds or more ends with a Timeline of at
most 20 rows, each covering an equal slice of the run:

```text
Timeline (1 second per row):
  time  req/s  p95 ms  errors  5xx
  0-1s    497   7.465       0    2
  1-2s    501   7.342       0    4
  2-3s    501   7.394       0    5
  3-4s    500   7.309       0    0
  4-5s    496   7.240       0    5
```

A 10-minute run gets 20 rows of 30 seconds. `p95` in a row is over every
response in that slice, not an average of seconds. Shorter runs print no
Timeline, so a quick `pelt <url>` looks as it always has. A run is timed
until its last request ends, so a `-d 20s` run lasts a little longer, such
as 20.004s; the Timeline folds a last part-second shorter than half a second
into the row before it, so those few ms don't show as a row of their own.

**In JSON**, `-o json` adds a `series` array with one object per second:

```json
"series": [
  {"t": 0, "stage": null, "target": null, "active": 3, "requests": 497, "responses": 497,
   "errors": 0, "s2xx": 495, "s3xx": 0, "s4xx": 0, "s5xx": 2, "rps": 497,
   "p50": 5.24, "p95": 7.465, "p99": 8.112, "max": 10.461}
]
```

**As CSV**, `-series out.csv` writes the same rows with a header, for a
spreadsheet or a plotting script. The file is opened before the run, so a bad
path fails before any load is sent (exit 1).

```text
t,stage,target,active,requests,responses,errors,s2xx,s3xx,s4xx,s5xx,rps,p50,p95,p99,max
0,,,3,497,497,0,495,0,0,2,497,5.24,7.465,8.112,10.461
4,,,0,252,252,0,247,0,0,5,495.73,5.02,7.24,8.136,8.17
```

| Column | Meaning |
|--------|---------|
| `t` | Second since the start: 0, 1, 2 ... |
| `stage`, `target` | With [load stages](#load-stages): the stage and its target in the middle of the second. Empty (`null` in JSON) without stages. |
| `active` | Requests in flight at the end of the second |
| `requests`, `responses`, `errors` | Requests that finished; those that got a response; transport errors |
| `s2xx` ... `s5xx` | Responses by status class |
| `rps` | Requests that finished in the second, per second of run time. Equal to `requests` except in the last row, which covers only the part of a second the run lasted (252 requests in 0.508 s is 495.73/s). |
| `p50`, `p95`, `p99`, `max` | Latency in ms of the responses that finished in the second; empty (`null`) when there were none |

The keys and columns are stable; flow mode adds `iterations`, `aborted` and
`check_fails` ([flow series](flows.md#per-second-results)). Keeping the
series costs 4 bytes per response (its finish time, beside its latency) and
about 100 bytes per second of run.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Finished, and every threshold passed |
| `1` | Runtime error, for example an unreadable `-body-file` or a `-series` file that cannot be created |
| `2` | Bad flags, config, stages or threshold; nothing was sent |
| `3` | A threshold was missed, or no request got a response (in flow mode, no iteration completed) |
| `130` | Interrupted with Ctrl-C (report still printed, thresholds still shown) |

An interrupted run exits `130` even when a threshold failed, because the
person stopped it.

## Full sample output

```text
$ pelt -n 50000 -c 1000 http://127.0.0.1:8080/
Target:       GET http://127.0.0.1:8080/
Load:         50000 requests, 1000 workers, no rate limit
Elapsed:      916ms

Requests:     50000 completed, 50000 responses, 0 errors (0.00%)
Throughput:   54598.3 requests/sec, 3.3 MiB/sec read
Connections:  632 opened, 49368 requests reused a connection

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

**What latency means here.** The clock starts just before `client.Do` and
stops after the response body has been read to EOF, which is what a real
client experiences. Only requests that got an HTTP response contribute
latency samples. A 500 is a response (it shows under status codes); a
timeout or refused connection is an error.

**Percentiles** use the nearest-rank method on the full, sorted set of
samples, so p99 is always a latency that was actually observed. Keeping every
sample costs 8 bytes per request (8 MB per million), which is cheap for an
exact answer.

**Connections** shows TCP connections opened vs requests that reused one. See
[why the transport tuning matters](design.md#why-the-transport-tuning-matters).

## JSON output

`-o json` prints the same report as one JSON object on stdout; the live
progress line goes to stderr, so the output pipes straight into `jq`. A run
of 500 requests, with the histogram and series trimmed:

```json
{
  "target": "http://127.0.0.1:8080/",
  "method": "GET",
  "concurrency": 20,
  "requests_configured": 500,
  "interrupted": false,
  "passed": true,
  "elapsed_ms": 168.624,
  "total": 500,
  "responses": 500,
  "errors": 0,
  "requests_per_sec": 2965.17,
  "bytes_read": 31718,
  "connections_opened": 20,
  "connections_reused": 480,
  "latency_ms": {
    "min": 2.328, "mean": 6.485, "stddev": 2.269, "max": 11.011,
    "percentiles": {"p50": 6.32, "p90": 9.664, "p95": 10.034, "p99": 10.726}
  },
  "histogram": [{"up_to_ms": 3.196, "count": 49}, ...],
  "status_codes": {"200": 494, "500": 6},
  "error_breakdown": {},
  "series": [{"t": 0, "requests": 500, ...}]
}
```

- `total` is `responses` plus `errors`. A response is any HTTP reply, 4xx and
  5xx included; an error got no reply. `error_breakdown` groups errors by
  kind: `timeout`, `connection refused`, `connection reset`,
  `out of local ports`, `too many open files`, `dns lookup failed`,
  `tls error` or `other: <message>`.
- `latency_ms` and `histogram` cover responses only, and are left out when
  there were none.
- Set only when they apply: `requests_configured`, `duration_configured`,
  `rate_limit`, `aborted` (requests cut off by Ctrl-C), `fail_reason`,
  `thresholds` ([Thresholds](#thresholds)), and `stages_configured` and
  `stages` ([Load stages](#load-stages)). `passed` and `series`
  ([Per-second results](#per-second-results)) are always there.

## Stopping a run early

The `-d` deadline only stops *dispatching*; requests already in flight
finish, so the tail of the run is still measured. Ctrl-C stops dispatching
*and* aborts in-flight requests. Aborted requests are counted separately, not
as errors, and the partial report is printed. A second Ctrl-C kills the
process.

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

## Reading the numbers

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

The sample numbers in these docs were taken with the client and the test
server on the same 4-vCPU machine, so they mostly measure that machine. For a
real benchmark, run the tester on a separate host.

## Use Pelt from AI agents

This repo ships an [Agent Skill](https://agentskills.io) at
[`.agents/skills/pelt/SKILL.md`](../.agents/skills/pelt/SKILL.md). It tells a
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
