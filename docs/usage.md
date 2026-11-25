# Using Pelt

[Back to README](../README.md)

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

## Examples

```sh
# 30 seconds at a steady 2,000 req/s
pelt -d 30s -c 200 -rate 2000 http://127.0.0.1:8080/

# POST JSON, machine-readable output
pelt -n 5000 -c 100 -m POST -H 'Content-Type: application/json' \
  -body '{"hello":"world"}' -o json http://127.0.0.1:8080/api | jq .latency_ms
```

## Exit codes

`0` finished, `130` interrupted with Ctrl-C (report still printed), `2` bad
flags or config, `1` other errors.

## Full sample output

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
