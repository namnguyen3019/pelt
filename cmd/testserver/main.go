// Command testserver is a small local HTTP server to point pelt at.
// It can add artificial latency and fail a share of requests, which makes
// the latency histogram and the status code breakdown easy to try out.
//
//	testserver -addr 127.0.0.1:8080 -delay 5ms -jitter 10ms -error-rate 0.01
//
// With -app shop it serves a tiny web shop instead (log in, products, cart,
// checkout, orders) for trying `pelt run` flows; see internal/testshop.
//
//	testserver -app shop -addr 127.0.0.1:8080 -delay 2ms -jitter 8ms
package main

import (
	"flag"
	"log"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/namnguyen3019/pelt/internal/testshop"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	app := flag.String("app", "", "what to serve: empty for a fixed body on every path, or \"shop\"")
	delay := flag.Duration("delay", 0, "fixed delay added to every response")
	jitter := flag.Duration("jitter", 0, "extra random delay, uniform in [0, jitter)")
	errRate := flag.Float64("error-rate", 0, "fraction of requests answered with 500 (0..1)")
	size := flag.Int("size", 64, "response body size in bytes (plain mode only)")
	flag.Parse()

	var h http.Handler
	switch *app {
	case "":
		body := []byte(strings.Repeat("x", *size))
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Write(body)
		})
	case "shop":
		h = testshop.New()
	default:
		log.Fatalf("unknown -app %q (want empty or shop)", *app)
	}

	http.Handle("/", slowAndFlaky(h, *delay, *jitter, *errRate))

	name := "plain"
	if *app != "" {
		name = *app
	}
	log.Printf("listening on http://%s (app=%s delay=%v jitter=%v error-rate=%v)", *addr, name, *delay, *jitter, *errRate)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

// slowAndFlaky wraps h with the -delay, -jitter and -error-rate behaviour.
func slowAndFlaky(h http.Handler, delay, jitter time.Duration, errRate float64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wait := delay
		if jitter > 0 {
			wait += rand.N(jitter)
		}
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-r.Context().Done(): // client gave up
				return
			}
		}
		if errRate > 0 && rand.Float64() < errRate {
			http.Error(w, "injected failure", http.StatusInternalServerError)
			return
		}
		h.ServeHTTP(w, r)
	})
}
