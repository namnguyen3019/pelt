package main

import (
	"flag"

	"github.com/namnguyen3019/pelt/internal/loadtest"
)

// networkFlags registers the TLS and proxy flags both modes share.
func networkFlags(fs *flag.FlagSet) *loadtest.Network {
	n := &loadtest.Network{}
	fs.BoolVar(&n.Insecure, "insecure", false, "skip verifying the server's TLS certificate (self-signed staging certs)")
	fs.StringVar(&n.CAFile, "cacert", "", "PEM file of CA certificates to trust, on top of the system's")
	fs.StringVar(&n.CertFile, "cert", "", "PEM client certificate, for servers that require one (with -key)")
	fs.StringVar(&n.KeyFile, "key", "", "PEM private key for -cert")
	fs.StringVar(&n.Proxy, "proxy", "", "send requests through this proxy, e.g. http://127.0.0.1:3128 (default: HTTPS_PROXY / HTTP_PROXY)")
	return n
}
