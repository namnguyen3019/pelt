# Flow mode: scripted user journeys

[Back to README](../README.md)

`pelt <url>` hammers one endpoint. `pelt run <file>` runs **user flows**
instead: each virtual user logs in once, keeps its own cookies, and loops
through weighted journeys such as browse and checkout, carrying values like a
token or an order id from one request to the next. The report gets a row per
step and a row per flow.

```sh
pelt run examples/shop.json                 # load settings from the file
pelt run examples/shop.json -u 200 -d 5m    # flags override the file's load block
pelt run examples/shop.json -i 1000 -o json # stop after 1,000 iterations, JSON out
pelt run examples/shop.json -dry-run        # one user, one pass, print every request
pelt run examples/shop.json -base https://staging.example.com
```

Only point it at servers you own or have permission to test. Flows usually
write data (carts, orders, sign-ups), so run them against a test system.

- [Try it locally](#try-it-locally)
- [The scenario file](#the-scenario-file)
- [Steps](#steps)
- [Variables and placeholders](#variables-and-placeholders)
- [Captures](#captures)
- [Checks and failures](#checks-and-failures)
- [Think time](#think-time)
- [Data file](#data-file)
- [How the users run](#how-the-users-run)
- [Flags](#flags)
- [Reading the report](#reading-the-report)
- [Debugging a file with -dry-run](#debugging-a-file-with--dry-run)
- [Not in this version yet](#not-in-this-version-yet)

## Try it locally

The test server has a small shop built in (log in, products, cart, checkout,
orders), and [`examples/shop.json`](../examples/shop.json) is a journey for
it.

```sh
go build -o bin/ ./cmd/...

# terminal 1: the shop, with 2 to 10 ms of latency and 0.2% injected 500s
./bin/testserver -app shop -addr 127.0.0.1:8080 -delay 2ms -jitter 8ms -error-rate 0.002

# terminal 2
./bin/pelt run examples/shop.json -dry-run
./bin/pelt run examples/shop.json -u 50 -d 20s
```

## The scenario file

A scenario is one JSON file. Unknown keys are an error, so a typo fails
before the run starts instead of being ignored.

```json
{
  "name": "shop",
  "base_url": "http://127.0.0.1:8080",
  "load": { "users": 20, "duration": "30s", "timeout": "10s" },
  "defaults": {
    "headers": { "Accept": "application/json" },
    "think": "200ms-1s"
  },
  "vars": { "page_size": 20 },
  "data": { "file": "users.csv", "mode": "unique" },

  "setup": [
    {
      "name": "login",
      "method": "POST",
      "url": "/login",
      "json": { "username": "{{username}}", "password": "{{password}}" },
      "check": { "status": 200 },
      "capture": [ { "json": "token", "as": "token" } ]
    }
  ],

  "flows": [
    {
      "name": "browse",
      "weight": 7,
      "steps": [
        { "name": "list products", "url": "/products?limit={{page_size}}",
          "check": { "status": 200 },
          "capture": [ { "json": "items[0].id", "as": "product_id" } ] },
        { "name": "view product", "url": "/products/{{product_id}}",
          "check": { "status": 200 } }
      ]
    },
    {
      "name": "checkout",
      "weight": 3,
      "steps": [
        { "name": "list products", "url": "/products?limit={{page_size}}",
          "check": { "status": 200 },
          "capture": [ { "json": "items[1].id", "as": "product_id" } ] },
        { "name": "add to cart", "method": "POST", "url": "/cart",
          "headers": { "Authorization": "Bearer {{token}}" },
          "json": { "product_id": "{{product_id}}", "qty": 1 },
          "check": { "status": [200, 201] } },
        { "name": "checkout", "method": "POST", "url": "/checkout",
          "headers": { "Authorization": "Bearer {{token}}" },
          "check": { "status": 201 },
          "capture": [ { "json": "order_id", "as": "order_id" } ],
          "think": "0s" },
        { "name": "order status", "url": "/orders/{{order_id}}",
          "check": { "status": 200 } }
      ]
    }
  ]
}
```

| Key | Meaning |
|-----|---------|
| `name` | Shown in the report. |
| `base_url` | Prefix for step URLs that start with `/`. `-base` replaces it. |
| `load` | `users`, `duration` (like `30s` or `2m`), `iterations` (total, across all users) and `timeout` (per request). Flags override each one. |
| `defaults` | `headers` sent on every step, `think` time after every step, and `cookies` (`false` turns the per-user cookie jar off). |
| `vars` | Fixed values for placeholders. Strings, numbers or booleans. |
| `data` | A CSV file whose columns become variables, one row per user. See [Data file](#data-file). |
| `setup` | Steps each user runs once, before any flow. Typically a login. |
| `flows` | Named, weighted lists of steps. Each iteration picks one flow by weight and runs its steps in order. |

`weight` defaults to 1. With weights 7 and 3, about 70% of iterations browse
and 30% check out.

## Steps

A step is one request.

| Key | Meaning |
|-----|---------|
| `name` | Groups the step's numbers in the report, so `/products/17` and `/products/42` land on one row. Defaults to `METHOD url`. Must be unique within its flow. |
| `method` | Defaults to `GET`. |
| `url` | Relative to `base_url` when it starts with `/`, or a full `http://` or `https://` address. |
| `headers` | Merged over `defaults.headers`; a step's value wins. |
| `body` | A string body. |
| `body_file` | Read the body from a file, relative to the scenario file. |
| `json` | A JSON body, written inline. Sets `Content-Type: application/json` unless a header already does. |
| `check` | `{"status": 200}` or `{"status": [200, 201]}`. |
| `capture` | Values to pull out of the response; see [Captures](#captures). |
| `think` | Pause after this step, overriding `defaults.think`. |
| `on_fail` | `abort` (default) or `continue`; see [Checks and failures](#checks-and-failures). |

Use at most one of `body`, `body_file` and `json`.

## Variables and placeholders

`{{name}}` works in the URL, header values and the body (including string
values inside `json`). A value is looked up in this order:

1. values captured earlier in this iteration,
2. values captured in setup (they last for the user's whole run, like a login token),
3. the user's row from the data file,
4. `vars`.

The loader checks every placeholder before the run starts: a name that is
not set by `vars`, the data file, a setup capture or a capture in an
**earlier** step of the same flow is an error, so `{{prodcut_id}}` fails
fast.

Built-ins:

| Placeholder | Value |
|-------------|-------|
| `{{$vu}}` | The user's number, from 1. |
| `{{$iteration}}` | The user's iteration count, from 1. |
| `{{$uuid}}` | A random UUID (version 4). |
| `{{$rand 1 100}}` | A random whole number between the two, inclusive. |
| `{{$env NAME}}` | The environment variable `NAME`, read once at load. An unset variable is an error. Use this for secrets rather than writing them in the file. |

Values are escaped for where they land: inside `json` they are JSON-escaped,
in a URL path they are path-escaped, and after the `?` they are
query-escaped. Header values and `body` text are inserted as is. A
placeholder at the very start of a URL is inserted as is too, so
`"url": "{{api}}/items"` can hold a whole address.

## Captures

Each capture has `as` (the variable name) and one source:

| Source | Example | Takes |
|--------|---------|-------|
| `json` | `"json": "items[0].id"` | A dot path into a JSON body, with `[n]` indexes. Strings come out without quotes; objects and arrays as compact JSON. |
| `header` | `"header": "Location"` | A response header. |
| `regex` | `"regex": "order-(\\d+)"` | A Go regular expression over the body: group 1 if there is one, else the whole match. |
| `status` | `"status": true` | The status code. |

A capture that finds nothing (a missing path, a `null`, no match, a missing
header) counts as a failed check on that step.

## Checks and failures

A step **fails** when its `check.status` does not match or one of its
captures finds nothing. A failure is counted against the step, and by default
the rest of that iteration is skipped and the user starts the next one, so a
checkout never runs with an empty cart id. `"on_fail": "continue"` on a step
keeps going instead; later placeholders that depended on a failed capture
render as empty text.

A transport error (timeout, refused connection) always aborts the iteration.

A step with no `check` accepts any status, including 500. Add a check to
every step whose status matters.

If **setup** fails, the user waits (1s, 2s, then 3s) and tries the whole
setup again, up to 4 attempts. A user that never gets through sits out the
rest of the run, and the report says how many did. If every user gives up,
the run ends early.

## Think time

`think` is a fixed pause (`"1s"`) or a uniform random range (`"500ms-2s"`)
after a step, set in `defaults` or per step. It includes the last step of a
flow, so it also spaces out iterations. At the end of a `-d` run (or on
Ctrl-C) any think time in progress is cut short.

## Data file

```json
"data": { "file": "users.csv", "mode": "unique" }
```

```text
username,password
user001,pass001
user002,pass002
```

The first row names the columns, which become variables. In `unique` mode
(the only mode for now) user *n* gets row *n*, so each user logs in as a
different account. The run refuses to start if there are fewer rows than
users. The path is relative to the scenario file.

## How the users run

- **Closed model.** `-u` users each loop: take an iteration, run it, think,
  repeat. A user that is thinking or waiting on a response is not sending, so
  a slower server gets fewer requests. This is the same caveat as
  [single-URL mode](usage.md#reading-the-numbers). A target-rate (open) mode
  is planned.
- **Run length.** `-d` stops users from starting new steps after that long;
  `-i` stops after that many iterations in total. With both, whichever comes
  first wins. `-d 0` clears a duration set in the file.
- **Connections.** All users share one tuned connection pool sized to `-u`,
  the same transport single-URL mode uses, so connections are reused. Each
  user has its own `http.Client` and cookie jar, so a session cookie set by
  one user's login is never sent by another user.
- **Repeatable.** Flow picks, think times, `$rand` and `$uuid` come from a
  random source per user seeded by `-seed`. The report prints the seed, so a
  run's sequence of choices can be repeated (server timing still varies).
- **Stopping.** At the `-d` deadline a request in flight finishes, remaining
  steps and think time are skipped, and that iteration counts as **cut**, not
  failed. Ctrl-C does the same but also aborts requests in flight; the
  partial report still prints and the exit code is 130.

## Flags

```text
pelt run [flags] <scenario.json>
```

Flags may go before or after the file name.

| Flag | Default | Meaning |
|------|---------|---------|
| `-u` | file, else 1 | Virtual users. |
| `-d` | file | Run for this long, e.g. `2m`. |
| `-i` | file | Stop after this many iterations in total. |
| `-t` | file, else 10s | Per-request timeout, including reading the body. |
| `-base` | file | Base URL, replacing `base_url`. |
| `-seed` | random | Seed for flow picks, think time, `$rand` and `$uuid`. |
| `-o` | text | `text` or `json`. |
| `-quiet` | false | Hide the live progress line (iterations, iterations/sec, failed checks). |
| `-http2` | false | Allow HTTP/2 over TLS. |
| `-dry-run` | false | Run setup and each flow once as one user and print everything. |

Exit codes: `0` finished, `3` no iteration completed (the report ends with
`Result: FAIL (no iteration completed)`, and JSON has `"passed": false`),
`130` interrupted, `2` bad file or flags (with the field, or the line and
column), `1` other errors or a failed `-dry-run`. `0` does not mean every
check passed; read `failed_checks` in the report. Thresholds that fail a flow
run on latency, errors or checks are not in flow mode yet; single-URL mode
has them ([thresholds](usage.md#thresholds)).

## Reading the report

From `testserver -app shop -delay 2ms -jitter 8ms -error-rate 0.002`, on the
same machine:

```text
$ pelt run examples/shop.json -u 50 -d 20s -seed 7
Scenario:     shop (examples/shop.json)
Load:         50 users, for 20s, seed 7
Elapsed:      20.002s

Iterations:   726 started: 692 completed, 5 aborted, 29 cut by the end of the run (36.3/sec)
Requests:     1914 completed, 1914 responses, 0 errors (0.00%), 5 failed checks
Throughput:   95.7 requests/sec, 31.6 KiB/sec read
Connections:  50 opened, 1864 requests reused a connection

Flow      weight  started     done  aborted    cut    flow p50    flow p95    flow p99    wall p50
browse         7      497      475        2     20      13.444      18.863      20.763    1203.507
checkout       3      229      217        3      9      28.069      35.596      37.050    1758.600
  (times in ms; flow time is request time only, wall time includes think time)

Step                         reqs  errors  failed        p50        p90        p95        p99        max
setup / login                  50       0       0      6.840      9.999     11.056     11.603     11.603
browse / list products        497       0       0      6.671      9.918     10.510     10.879     11.520
browse / view product         477       0       2      6.722      9.895     10.558     11.033     12.065
checkout / list products      229       0       0      6.824     10.152     10.561     10.981     11.430
checkout / add to cart        224       0       1      6.697      9.960     10.672     10.813     10.961
checkout / checkout           219       0       1      6.973     10.144     10.618     10.876     11.137
checkout / order status       218       0       1      6.875      9.853     10.628     10.895     11.377
  (times in ms; failed = status check or capture failed)

Aborted at step:
       2  browse / view product
       1  checkout / add to cart
       1  checkout / checkout
       1  checkout / order status

Status codes:
  200  1691
  201  218
  500  5
```

- **Iterations**: every started iteration ends as completed, aborted (a
  failure skipped the rest) or cut (the run ended part way).
- **Flows**: flow time adds up the request times of each completed
  iteration and leaves out think time, so it shows what the server cost a
  user. Wall time is the same iterations end to end, think time included.
- **Steps**: `reqs` is requests that completed; `errors` had no HTTP
  response; `failed` got a response but failed a check or capture.
  Percentiles are over responses. `list products` in `browse` and in
  `checkout` are separate rows.
- **Aborted at step** shows where failures cut iterations short. Here the
  five injected 500s each stopped one iteration.

With no think time, the same shop on the same machine served about 37,800
requests per second across 200 users, each doing full journeys with logins,
cookies and JSON captures.

### JSON output

`-o json` prints one object with `"mode": "flow"`, the overall totals in the
same keys as single-URL mode (`total`, `responses`, `errors`,
`requests_per_sec`, `status_codes`, `error_breakdown`,
`connections_opened`, `connections_reused`), plus the run details, iteration counts and one entry per flow and per step. Trimmed to one flow and one step, with numbers rounded:

```json
{
  "mode": "flow",
  "scenario": "shop",
  "file": "examples/shop.json",
  "users": 20,
  "duration_configured": "8s",
  "seed": 7,
  "interrupted": false,
  "elapsed_ms": 8000.87,
  "iterations": {"started": 115, "completed": 104, "aborted": 0, "cut": 11, "per_sec": 14.37},
  "setup_failed_users": 0,
  "total": 312,
  "responses": 312,
  "errors": 0,
  "failed_checks": 0,
  "requests_per_sec": 39.0,
  "flows": [
    {"name": "checkout", "weight": 3, "started": 38, "completed": 36, "aborted": 0, "cut": 2,
     "aborted_at": {},
     "flow_time_ms": {"min": 16.92, "mean": 27.07, "stddev": 4.92, "max": 35.95,
                      "percentiles": {"p50": 27.96, "p95": 35.49, "p99": 35.95}},
     "wall_time_ms": {"min": 1118.24, "mean": 1719.63, "stddev": 418.06, "max": 2847.9,
                      "percentiles": {"p50": 1641.26, "p95": 2694.33, "p99": 2847.9}}}
  ],
  "steps": [
    {"flow": "checkout", "name": "checkout", "requests": 36, "responses": 36, "errors": 0,
     "failed_checks": 0, "aborted_iterations": 0, "requests_per_sec": 4.5,
     "latency_ms": {"min": 2.53, "mean": 7.14, "stddev": 2.4, "max": 10.56,
                    "percentiles": {"p50": 7.46, "p90": 10.17, "p95": 10.55, "p99": 10.56}},
     "status_codes": {"201": 36}, "error_breakdown": {}}
  ]
}
```

`flow_time_ms`, `wall_time_ms` and `latency_ms` are left out when there are
no samples.

## Debugging a file with -dry-run

`-dry-run` runs setup and then each flow once, in file order, as user 1 with
no think time. It prints each request as sent (URL, headers including
cookies, body), the response, and what every check and capture found, then
exits without a load report. It exits 1 if any step failed.

```text
$ pelt run examples/shop.json -dry-run
Dry run of examples/shop.json: 1 user, setup and then each flow once, no think time.

setup
  login
    POST http://127.0.0.1:8080/login
    Accept: application/json
    Content-Type: application/json
    body {"username":"user001","password":"pass001"}
    -> 200 OK in 4.91ms, 54 bytes
    response {"token":"3e1b3daa3d514d658b65d762","user":"user001"}
    check ok: status 200
    capture token = "3e1b3daa3d514d658b65d762"

browse (weight 7)
  list products
    GET http://127.0.0.1:8080/products?limit=20
    Accept: application/json
    Cookie: session=3e1b3daa3d514d658b65d762
    -> 200 OK in 7.4ms, 814 bytes
    response {"items":[{"id":1,"name":"Paddle 1","price":42.5},{"id":2,...  (first 300 bytes) ... (513 more bytes)
    check ok: status 200
    capture product_id = "1"
  ...

All steps passed.
```

The dry run prints headers and bodies in full, tokens included, so don't
paste its output anywhere public.

## Not in this version yet

Planned for later versions, roughly in this order:

- Ramp-up (`-ramp`), `sequential` and `random` data modes, and checks on the
  body (`body_contains`, JSON field equals, max latency).
- A target-rate (open) model, `-rate` iterations per second, counting
  iterations dropped when no user is free; thresholds in the file, per flow
  and per step, that fail the run for CI (single-URL mode already has
  `-threshold`); per-step histograms in JSON.
- YAML scenario files, load stages, loops and conditional steps.

Single-URL mode (`pelt [flags] <url>`) is unchanged and remains the fastest
way to hammer one endpoint.
