// Package loadtest contains the load generator: configuration, the HTTP
// transport setup, the dispatcher and worker pool, and the raw results.
package loadtest

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/namnguyen3019/pelt/internal/stages"
)

// Config describes one load test run.
type Config struct {
	URL    string
	Method string
	Header http.Header
	Body   []byte

	// Requests is the total number of requests to send. 0 means no limit,
	// in which case Duration must be set.
	Requests int
	// Duration stops dispatching new requests after this long. 0 means no
	// limit, in which case Requests must be set. If both are set, whichever
	// is reached first ends the run.
	Duration time.Duration

	// Concurrency is the number of worker goroutines, i.e. the maximum
	// number of requests in flight at once.
	Concurrency int
	// Timeout bounds each request, including reading the response body.
	Timeout time.Duration
	// RateLimit caps the overall request rate in requests per second.
	// 0 means as fast as the workers can go.
	RateLimit float64

	// HTTP2 lets the client negotiate HTTP/2 over TLS. By default the tester
	// forces HTTP/1.1 so that -c maps directly onto TCP connections.
	HTTP2 bool
	// Untuned uses Go's stock transport limits (2 idle connections per
	// host) instead of sizing the pool to Concurrency. Useful only to show
	// why tuning matters; see docs/design.md.
	Untuned bool

	// Stages, when set, change the load over the run and set its length,
	// so Duration must be 0; Requests still stops the run early. Level
	// stages move the number of requests in flight, and Concurrency becomes
	// their highest target. Rate stages move requests per second, so
	// RateLimit must be 0, and Concurrency caps requests in flight.
	Stages *stages.Plan
}

// Validate fills in defaults and reports configuration errors.
func (c *Config) Validate() error {
	u, err := url.Parse(c.URL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL must start with http:// or https://, got %q", c.URL)
	}
	if u.Host == "" {
		return errors.New("URL has no host")
	}
	if c.Method == "" {
		c.Method = http.MethodGet
	}
	if c.Requests < 0 {
		return errors.New("number of requests cannot be negative")
	}
	if c.Duration < 0 {
		return errors.New("duration cannot be negative")
	}
	if c.Stages != nil {
		if c.Duration > 0 {
			return errors.New("stages set the run length, so a duration cannot be set too")
		}
		switch c.Stages.Kind {
		case stages.Level:
			c.Concurrency = int(math.Ceil(c.Stages.Max()))
		case stages.Rate:
			if c.RateLimit > 0 {
				return errors.New("rate stages set the request rate, so a rate limit cannot be set too")
			}
		}
	}
	if c.Requests == 0 && c.Duration == 0 && c.Stages == nil {
		return errors.New("set a number of requests, a duration, or both")
	}
	if c.Concurrency < 1 {
		return errors.New("concurrency must be at least 1")
	}
	// No point starting more workers than there are requests to send.
	if c.Requests > 0 && c.Concurrency > c.Requests {
		c.Concurrency = c.Requests
	}
	if c.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if c.RateLimit < 0 {
		return errors.New("rate limit cannot be negative")
	}
	return nil
}
