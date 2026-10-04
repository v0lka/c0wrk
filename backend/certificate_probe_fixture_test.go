package backend

import (
	"crypto/tls"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Certificate probes disconnect without sending HTTP. TLS 1.2 completes the
// server handshake before the client returns, unlike TLS 1.3's final-flight
// scheduling window. These fixtures test SPKI extraction, not TLS versions.
func newCertificateProbeServer(t *testing.T) *httptest.Server {
	t.Helper()
	diagnostics := &apiDiagnosticBuffer{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Config.ErrorLog = log.New(diagnostics, "", 0)
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(func() {
		srv.Close()
		diagnostics.mu.Lock()
		defer diagnostics.mu.Unlock()
		if diagnostics.Len() != 0 {
			t.Errorf("certificate probe server diagnostics = %q, want zero messages after a completed handshake", diagnostics.String())
		}
	})
	return srv
}
