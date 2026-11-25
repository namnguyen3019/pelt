package loadtest

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// newTransport builds the http.Transport shared by every worker.
//
// The important setting is MaxIdleConnsPerHost. Go's default is 2: after a
// response, the connection goes back to an idle pool that only keeps two
// connections per host, and the rest are closed. With 500 workers hitting a
// single host, roughly 498 connections would be torn down after every
// request and re-dialled for the next one. That means a TCP (and possibly TLS)
// handshake per request, thousands of sockets stuck in TIME_WAIT, and in the
// worst case running out of local ports. The test would end up measuring the
// client's connection churn rather than the server.
//
// Sizing the idle pool to the worker count lets each worker keep reusing one
// warm keep-alive connection for the whole run.
func newTransport(cfg Config) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()

	if cfg.Untuned {
		t.MaxIdleConnsPerHost = 0 // 0 means http.DefaultMaxIdleConnsPerHost (2)
		return t
	}

	t.DialContext = (&net.Dialer{
		Timeout:   cfg.Timeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	t.MaxIdleConns = cfg.Concurrency        // across all hosts (redirects may add hosts)
	t.MaxIdleConnsPerHost = cfg.Concurrency // one warm connection per worker
	t.MaxConnsPerHost = 0                   // no cap: concurrency is controlled by the worker count
	t.IdleConnTimeout = 90 * time.Second
	t.TLSHandshakeTimeout = cfg.Timeout

	// Ask for the raw body. With compression on, Go transparently gunzips
	// responses, which adds client CPU time to every latency sample.
	t.DisableCompression = true

	if !cfg.HTTP2 {
		// HTTP/2 multiplexes many requests over one connection, which hides
		// how a server copes with many concurrent connections. A non-nil,
		// empty TLSNextProto map turns HTTP/2 off.
		t.ForceAttemptHTTP2 = false
		t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}
	return t
}
