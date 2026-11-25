# How Pelt works

[Back to README](../README.md)

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

**Load stages.** With `-stages`, the dispatcher also follows a plan from
`internal/stages`, a pure package that says what the target is at any
moment. For rate stages it keeps the absolute schedule, with a moving rate:
request *i* is due when the area under the rate curve reaches *i* + 0.5, a
quadratic solved per stage. A ramp from 0 therefore starts at once instead
of waiting for the rate to climb, and a stage at `0/s` is simply skipped
over. For concurrency stages, Pelt starts as many workers as the highest
target, and the dispatcher adds a gate: it sends a job only while jobs sent
minus requests completed is below the target now. It counts both itself,
because a worker counts its request only after picking the job up, which
would let the dispatcher overshoot. When the gate is shut it waits for a
worker to signal that a request finished (a channel buffered by one, so
workers never block on it) or for a 100 ms tick, which picks up a rising
target. The hot path gains one non-blocking send per request, and only with
concurrency stages.

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
and the partial report is printed (see the
[sample](usage.md#stopping-a-run-early)). A second Ctrl-C kills the process,
because `signal.NotifyContext`'s `stop()` restores default signal handling.

**Per-second series.** Beside each latency, a worker keeps the time its
request finished, as a `uint32` of milliseconds since the start (4 bytes,
good for 49 days). It also adds to one shared row of counters per second
(`internal/series`), with atomic adds and no locks: rows live in chunks of
1,024 seconds that are allocated on first use with a compare-and-swap. A
sampler goroutine wakes at the end of every second, on an absolute schedule,
to note requests in flight. After the run, a counting pass groups the
latencies by finish second and sorts each group, which costs about what the
report's own sort does. That has to happen before the report sorts the
latencies for the overall percentiles, since sorting breaks the link between
a latency and its finish time.

For what latency and percentiles mean in the report, see
[Using Pelt](usage.md#full-sample-output).

## Why the transport tuning matters

Go's `http.DefaultTransport` keeps only **2 idle connections per host**
(`MaxIdleConnsPerHost`). With 500 workers hitting one host, when ~500
responses come back only 2 connections fit in the idle pool and the rest are
closed. Each of those workers then dials a brand-new TCP connection for its
next request. You end up measuring the client's connection churn, and you
leave thousands of sockets in `TIME_WAIT`, which can exhaust local ports.

Pelt sizes `MaxIdleConns` and `MaxIdleConnsPerHost` to `-c`, so every worker
keeps one warm keep-alive connection for the whole run. It also always drains
and closes response bodies, because a body that is not read to EOF cannot go
back to the pool. An `httptrace.ClientTrace` counts actual TCP dials
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
(5,965 for 5,000 workers in [this run](usage.md#reading-the-numbers)). Go's
transport races a new dial against a connection coming back to the idle pool;
if the pooled connection wins, the dial still completes and is kept for later.

## Project layout

```text
cmd/pelt/            CLI: flags, signal handling, progress line, output
cmd/testserver/      local target with configurable delay and error rate
internal/loadtest/   Config, tuned Transport, dispatcher + worker pool, Result
internal/flow/       flow mode: scenario file, templates, captures, virtual users
internal/testshop/   the small shop that testserver -app shop serves
internal/stats/      percentiles, mean/stddev, histogram (pure functions)
internal/series/     per-second counters, sampler, rows and spans (lock-free)
internal/stages/     load stages: parse, target at a time, rate schedule (pure)
internal/report/     text and JSON rendering, timeline and series CSV, threshold verdict
internal/threshold/  parse threshold expressions, compare measured values (pure)
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
- `internal/series`: counters shared by many goroutines across a chunk
  boundary, a request finishing at 1.999 s landing in second 1, rows adding
  up to the run's totals, empty seconds having no percentiles, the partial
  last second, and timeline spans.
- `internal/stages`: good and bad stage lists, the target at stage edges and
  midpoints, `0s` jumps, the stage a moment belongs to, `-ramp`, and the
  rate schedule against the area under the curve, across a `0/s` gap.
- `internal/loadtest` with stages: requests in flight never above the
  target on a ramp up and down, and none left at the end; a `2s:100/s` ramp
  sends about 100 requests; `-n` still stops a staged run early.
- `internal/threshold`: parsing every metric, unit and operator, rejected
  expressions with a readable reason, and the boundary where a value equals
  its target.
- `internal/report`: text and JSON output, the Thresholds block and verdict,
  the rule that a run with no responses fails, the Timeline, and stable
  series JSON keys and CSV columns.
- `cmd/pelt`: end-to-end run of the CLI against a local server, exit code 3
  for a missed threshold and for a dead target, bad flag and threshold
  handling, exit 2 for every stage conflict with nothing sent, `-ramp`
  building the same stages as its long form, and the `stages` JSON.
- `internal/flow`: scenario parsing and errors, `thresholds` lists at every
  level and their scopes (with the line and column of a bad entry), templates
  and escaping, captures, weighted flow picks, and runs against the test shop: per-user
  cookies, chained captures, abort vs `on_fail: continue`, setup retries,
  duration and Ctrl-C cut-off, and the dry run.
- `internal/flow` with stages: `load.stages` parsing and errors with their
  line, setup once per user with cookies kept while users park and wake, a
  ramp down to 0 that cuts no iteration, `-i` ending a staged run with users
  parked, and the dry run ignoring stages.
- `cmd/pelt` flow mode: `pelt run` end to end in text, JSON and dry-run,
  exit 3 and 0 for thresholds from the file and the command line,
  `-no-thresholds`, bad files, flags and threshold scopes, exit 2 for every
  stage conflict with nothing sent, stages from the file and from `-stages`,
  and `-ramp` building the same stages as its long form.

Thresholds are checked once, after the run, against the same numbers the
report prints. `internal/threshold` only parses and compares; the report
package measures each metric, which keeps the parser free of run types. In
flow mode `internal/flow` ties each threshold to the run, a flow or a step
(and rejects a metric that level does not have), and the report measures it
there.

Flow mode shares the tuned transport with single-URL mode. Each virtual user
gets its own `http.Client` and cookie jar on top of it and writes to its own
recorder, which are merged after the run, so the hot path takes no locks.
See [flows.md](flows.md#how-the-users-run).

**Flow stages.** In flow mode stages move users, not requests, so the
runner starts a goroutine for every user up to the highest target and lets
the target decide which may run. A controller ticks every 100 ms and stores
the target, rounded, in an atomic; when it changes, the controller closes a
channel and swaps in a new one, which wakes every waiting user at once
without a lock. User *n* runs setup the first time the target reaches *n*,
so logins happen once per user. Between iterations, a user above the target
parks on that channel, keeping its cookie jar, captures and random source,
and resumes when the target rises again. A user above the target that is
thinking stops thinking early; one in the middle of steps finishes them, so
a ramp down cuts no iteration short. The dispatcher closes a channel once it
stops handing out iterations, so parked users leave as soon as `-i` ends
the run. Users count as active only while running setup or an iteration.

## Ideas for later

- Open-loop mode that corrects for coordinated omission
- HDR histogram for constant memory on very long runs
- Flow mode: a target-rate (open) model
- Stopping a run early when a threshold is clearly missed
