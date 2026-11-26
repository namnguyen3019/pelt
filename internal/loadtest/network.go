package loadtest

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
)

// Network holds the connection options shared by both modes: how to trust
// the server, which client certificate to show, and which proxy to use.
// The zero value means Go's defaults: the system's trusted CAs, no client
// certificate, and a proxy from HTTPS_PROXY, HTTP_PROXY and NO_PROXY.
type Network struct {
	// Insecure skips verifying the server's certificate.
	Insecure bool
	// CAFile is a PEM file of CA certificates to trust, on top of the
	// system's.
	CAFile string
	// CertFile and KeyFile are a PEM client certificate and its key, for
	// servers that ask for one (mutual TLS). Set both or neither.
	CertFile, KeyFile string
	// Proxy is a proxy URL (http://, https:// or socks5://) for every
	// request. Empty uses the environment.
	Proxy string
}

// Build loads the files and parses the proxy. It returns a nil TLS config
// or proxy where the defaults apply, so the errors come before any load is
// sent.
func (n Network) Build() (*tls.Config, *url.URL, error) {
	var tc *tls.Config
	if n.Insecure || n.CAFile != "" || n.CertFile != "" || n.KeyFile != "" {
		tc = &tls.Config{InsecureSkipVerify: n.Insecure} //nolint:gosec // only when the user asks for it
	}
	if n.CAFile != "" {
		pem, err := os.ReadFile(n.CAFile)
		if err != nil {
			return nil, nil, fmt.Errorf("CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool() // Windows before Go 1.18, or no system store
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, nil, fmt.Errorf("CA file %s: no PEM certificates found", n.CAFile)
		}
		tc.RootCAs = pool
	}
	switch {
	case n.CertFile != "" && n.KeyFile == "":
		return nil, nil, errors.New("a client certificate needs its key too")
	case n.KeyFile != "" && n.CertFile == "":
		return nil, nil, errors.New("a client key needs its certificate too")
	case n.CertFile != "":
		cert, err := tls.LoadX509KeyPair(n.CertFile, n.KeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("client certificate: %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}

	var proxy *url.URL
	if n.Proxy != "" {
		u, err := url.Parse(n.Proxy)
		if err != nil {
			return nil, nil, fmt.Errorf("proxy: %w", err)
		}
		switch u.Scheme {
		case "http", "https", "socks5":
		default:
			return nil, nil, fmt.Errorf("proxy %q: want a URL starting with http://, https:// or socks5://", n.Proxy)
		}
		if u.Host == "" {
			return nil, nil, fmt.Errorf("proxy %q has no host", n.Proxy)
		}
		proxy = u
	}
	return tc, proxy, nil
}
