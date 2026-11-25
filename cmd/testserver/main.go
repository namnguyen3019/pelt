// Command testserver is a small local HTTP server to point pelt at.
// It can add artificial latency and fail a share of requests, which makes
// the latency histogram and the status code breakdown easy to try out.
//
//	testserver -addr 127.0.0.1:8080 -delay 5ms -jitter 10ms -error-rate 0.01
package main

import (
	"flag"
	"log"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	delay := flag.Duration("delay", 0, "fixed delay added to every response")
	jitter := flag.Duration("jitter", 0, "extra random delay, uniform in [0, jitter)")
	errRate := flag.Float64("error-rate", 0, "fraction of requests answered with 500 (0..1)")
	size := flag.Int("size", 64, "response body size in bytes")
	flag.Parse()

	body := []byte(strings.Repeat("x", *size))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		wait := *delay
		if *jitter > 0 {
			wait += rand.N(*jitter)
		}
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-r.Context().Done(): // client gave up
				return
			}
		}
		if *errRate > 0 && rand.Float64() < *errRate {
			http.Error(w, "injected failure", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write(body)
	})

	log.Printf("listening on http://%s (delay=%v jitter=%v error-rate=%v)", *addr, *delay, *jitter, *errRate)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
