# Pelt vs Locust

[Back to README](../README.md)

[Locust](https://docs.locust.io) is a popular open source load testing tool where
you describe user behaviour in Python. The two tools aim at different jobs:

| | Pelt | Locust |
|---|---|---|
| Install | One static binary, or `go install` | `pip install locust` (needs Python) |
| Defining a test | Command-line flags: one URL, method, headers, body | Python code: user classes with tasks, so logins, loops, conditions and any Python library |
| Load model | A fixed pool of workers (`-c`) sending back to back, with an optional global rate cap | Simulated users with wait times, a spawn rate to ramp up, and custom load shapes |
| Interface | Terminal: live progress line, then a text or JSON report | Web UI with live charts and load you can change mid-run; also runs headless |
| Scaling out | One process uses every core; no distributed mode | One process per core (Python's GIL); `--processes` and master/worker mode, across many machines |
| Throughput per process | About 54,600 req/s in the [sample run](usage.md#full-sample-output) | Up to about 16,000 req/s with `FastHttpUser` or 4,000 with `HttpUser`, best case, per Locust's docs |
| Results | Percentiles, histogram, status codes, error kinds, connections opened vs reused | Live stats and charts, CSV export |
| Protocols | HTTP and HTTPS | HTTP out of the box, any protocol with a custom client |

The throughput figures come from different hardware and different tests (Pelt:
client and server sharing one 4-vCPU machine; Locust: its own docs, one
process on one core), so read them as orders of magnitude, not a benchmark.

## When to use which

Use Pelt to hammer one endpoint and get throughput, latency percentiles and
connection behaviour in seconds, from a terminal or a CI job. Use Locust when
the test is a user journey (log in, browse, check out), when you want a web UI
to watch and steer the load, or when you need to spread load across many
machines.
