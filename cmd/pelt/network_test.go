package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// jsonResponses runs pelt with args and returns the exit code, the number
// of responses in its JSON report and stderr.
func jsonResponses(t *testing.T, args ...string) (code, responses int, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(append([]string{"-o", "json", "-quiet"}, args...), &out, &errb)
	var rep struct {
		Responses int `json:"responses"`
	}
	if code == 0 || code == exitFailed {
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatalf("report is not JSON: %v\n%s", err, out.String())
		}
	}
	return code, rep.Responses, errb.String()
}

func writePEM(t *testing.T, dir, name, typ string, der []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTLSFlags(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	dir := t.TempDir()
	ca := writePEM(t, dir, "ca.pem", "CERTIFICATE", srv.Certificate().Raw)

	// Without help, the test server's self-signed certificate is refused:
	// every request is an error, so nothing gets a response (exit 3).
	if code, n, _ := jsonResponses(t, "-n", "5", "-c", "1", srv.URL); code != exitFailed || n != 0 {
		t.Errorf("no flags: exit %d with %d responses, want exit 3 with none", code, n)
	}
	if code, n, stderr := jsonResponses(t, "-n", "5", "-c", "1", "-insecure", srv.URL); code != 0 || n != 5 {
		t.Errorf("-insecure: exit %d with %d responses (%s)", code, n, stderr)
	}
	if code, n, stderr := jsonResponses(t, "-n", "5", "-c", "1", "-cacert", ca, srv.URL); code != 0 || n != 5 {
		t.Errorf("-cacert: exit %d with %d responses (%s)", code, n, stderr)
	}
}

func TestClientCertificate(t *testing.T) {
	// A self-signed client certificate that the server trusts.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pelt-test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	clientCert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(clientCert)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.StartTLS()
	defer srv.Close()

	dir := t.TempDir()
	certFile := writePEM(t, dir, "client.pem", "CERTIFICATE", der)
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := writePEM(t, dir, "client.key", "EC PRIVATE KEY", keyDER)

	if code, n, _ := jsonResponses(t, "-n", "3", "-c", "1", "-insecure", srv.URL); code != exitFailed || n != 0 {
		t.Errorf("no client cert: exit %d with %d responses, want exit 3 with none", code, n)
	}
	if code, n, stderr := jsonResponses(t, "-n", "3", "-c", "1", "-insecure", "-cert", certFile, "-key", keyFile, srv.URL); code != 0 || n != 3 {
		t.Errorf("with -cert: exit %d with %d responses (%s)", code, n, stderr)
	}
}

func TestProxyFlag(t *testing.T) {
	// A forward proxy for plain HTTP gets the full target URL in the
	// request line; this one answers itself and counts.
	var seen atomic.Int64
	var target atomic.Value
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		target.Store(r.URL.String())
	}))
	defer proxy.Close()

	code, n, stderr := jsonResponses(t, "-n", "4", "-c", "1", "-proxy", proxy.URL, "http://pelt-target.invalid/x")
	if code != 0 || n != 4 || seen.Load() != 4 {
		t.Fatalf("exit %d, %d responses, proxy saw %d (%s)", code, n, seen.Load(), stderr)
	}
	if got := target.Load(); got != "http://pelt-target.invalid/x" {
		t.Errorf("proxy got request for %v", got)
	}
}

func TestNetworkFlagErrors(t *testing.T) {
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "x.txt")
	os.WriteFile(notPEM, []byte("hello"), 0o600)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-cacert", filepath.Join(dir, "missing.pem")}, "CA file"},
		{[]string{"-cacert", notPEM}, "no PEM certificates"},
		{[]string{"-cert", notPEM}, "needs its key"},
		{[]string{"-key", notPEM}, "needs its certificate"},
		{[]string{"-cert", notPEM, "-key", notPEM}, "client certificate"},
		{[]string{"-proxy", "ftp://x"}, "want a URL starting with"},
		{[]string{"-proxy", "http://"}, "has no host"},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		args := append(append([]string{}, c.args...), "http://127.0.0.1:1/")
		if code := run(args, &out, &errb); code != exitUsage || !strings.Contains(errb.String(), c.want) {
			t.Errorf("%v: exit %d, stderr %q; want exit 2 mentioning %q", c.args, code, errb.String(), c.want)
		}
		// Flow mode checks the same flags before running.
		errb.Reset()
		file := filepath.Join(dir, "f.json")
		os.WriteFile(file, []byte(`{"load":{"iterations":1},"flows":[{"name":"f","steps":[{"name":"s","url":"http://127.0.0.1:1/"}]}]}`), 0o600)
		if code := run(append([]string{"run", file}, c.args...), &out, &errb); code != exitUsage || !strings.Contains(errb.String(), c.want) {
			t.Errorf("run %v: exit %d, stderr %q; want exit 2 mentioning %q", c.args, code, errb.String(), c.want)
		}
	}
}

func TestFlowInsecure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	dir := t.TempDir()
	file := filepath.Join(dir, "f.json")
	os.WriteFile(file, []byte(`{"load":{"iterations":3},"flows":[{"name":"f","steps":[{"name":"s","url":"/","check":{"status":200}}]}]}`), 0o600)

	for _, tc := range []struct {
		flags []string
		code  int
	}{
		{nil, exitFailed},
		{[]string{"-insecure"}, exitOK},
	} {
		var out, errb bytes.Buffer
		args := append([]string{"run", file, "-base", srv.URL, "-quiet", "-o", "json", "-threshold", "errors<1%"}, tc.flags...)
		if code := run(args, &out, &errb); code != tc.code {
			t.Errorf("%v: exit %d, want %d (%s)", tc.flags, code, tc.code, errb.String())
		}
	}
}
