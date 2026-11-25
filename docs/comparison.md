# Pelt vs Locust

[Back to README](../README.md)

[Locust](https://docs.locust.io) is a popular open source load testing tool where
you describe user behaviour in Python. The two tools aim at different jobs:

| | Pelt | Locust |
|---|---|---|
| Install | One static binary, or `go install` | `pip install locust` (needs Python) |
| Defining a test | Command-line flags for one URL, or a JSON file of weighted user flows with setup, captured values and per-user cookies ([flows](flows.md)) | Python code: user classes with tasks, so logins, loops, conditions and any Python library |
| Load model | A pool of workers (`-c`) sending back to back, with an optional global rate cap; in flow mode, a fixed number of virtual users with think time | Simulated users with wait times, a spawn rate to ramp up, and custom load shapes |
| Changing load over a run | `-stages 30s:200,2m:200,30s:0` moves concurrency or request rate in straight lines between targets, or `-ramp 30s`; in flow mode, stages in the file or on the command line move the number of users | A spawn rate, or a Python `LoadTestShape` class whose `tick()` returns users and spawn rate |
| Interface | Terminal: live progress line, then a text or JSON report | Web UI with live charts and load you can change mid-run; also runs headless |
| Scaling out | One process uses every core; no distributed mode | One process per core (Python's GIL); `--processes` and master/worker mode, across many machines |
| Throughput per process | About 54,600 req/s in the [sample run](usage.md#full-sample-output) | Up to about 16,000 req/s with `FastHttpUser` or 4,000 with `HttpUser`, best case, per Locust's docs |
| Results | Percentiles, histogram, status codes, error kinds, connections opened vs reused | Live stats and charts, CSV export |
| Over time | One row per second, already aggregated (requests, status classes, errors, p50 to p99): a Timeline in the terminal, `series` in JSON, CSV with `-series` | `--csv` appends the current 10-second window's stats to a history file |
| Pass/fail for CI | `-threshold 'p95<300ms'` and the like (latency percentiles, errors, status codes, req/s), and in flow mode `thresholds` lists per flow and per step in the scenario file (plus failed checks and aborted iterations); exit 3 when one is missed, and 3 when nothing got a response | Exit 1 if any request failed; latency or ratio rules need a Python `quitting` hook that sets `process_exit_code` |
| Protocols | HTTP and HTTPS | HTTP out of the box, any protocol with a custom client |

The throughput figures come from different hardware and different tests (Pelt:
client and server sharing one 4-vCPU machine; Locust: its own docs, one
process on one core), so read them as orders of magnitude, not a benchmark.

## When to use which

Use Pelt to hammer one endpoint and get throughput, latency percentiles and
connection behaviour in seconds, from a terminal or a CI job. Pelt's
[flow mode](flows.md) also covers straight-line journeys (log in, browse,
check out) written as JSON, with a report per step. Use Locust when a journey
needs real code (branches, loops, other libraries), when you want a web UI to
watch and steer the load as it runs, or when you need to spread load across
many machines.
