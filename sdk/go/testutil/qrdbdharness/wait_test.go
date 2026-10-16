// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package qrdbdharness

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// shortTimeout keeps the negative-path waiters fast: it is several poll
// intervals, so every waiter probes repeatedly before giving up.
const shortTimeout = 400 * time.Millisecond

// newTLSServer starts an HTTPS test server and returns it with a pool that
// trusts it.
func newTLSServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, pool
}

func TestHTTPSClient(t *testing.T) {
	ca := newTestCA(t)
	kp, err := ca.Issue(t.TempDir(), WithSubject("admin", "system"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cert, err := kp.TLSCertificate()
	if err != nil {
		t.Fatalf("TLSCertificate: %v", err)
	}

	tests := []struct {
		name                     string
		cert                     *tls.Certificate
		wantGetClientCertificate bool
	}{
		{name: "ca only", cert: nil, wantGetClientCertificate: false},
		{name: "mutual tls", cert: &cert, wantGetClientCertificate: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := HTTPSClient(ca.Pool, tc.cert, 3*time.Second)
			if client.Timeout != 3*time.Second {
				t.Errorf("Timeout = %v, want 3s", client.Timeout)
			}
			transport, ok := client.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
			}
			if !transport.ForceAttemptHTTP2 {
				t.Error("ForceAttemptHTTP2 is false; qrdbd's admin API is HTTP/2")
			}
			if transport.TLSClientConfig.MinVersion != tls.VersionTLS13 {
				t.Errorf("MinVersion = %x, want TLS 1.3", transport.TLSClientConfig.MinVersion)
			}
			if transport.TLSClientConfig.RootCAs != ca.Pool {
				t.Error("RootCAs is not the supplied pool")
			}
			// The static Certificates field stays empty either way: a
			// supplied cert is carried via GetClientCertificate instead,
			// so it is presented on every handshake regardless of the
			// server's advertised AcceptableCAs (HTTPSClient's own doc
			// comment).
			if got := len(transport.TLSClientConfig.Certificates); got != 0 {
				t.Errorf("client certificates = %d, want 0 (carried via GetClientCertificate instead)", got)
			}
			hasGetClientCert := transport.TLSClientConfig.GetClientCertificate != nil
			if hasGetClientCert != tc.wantGetClientCertificate {
				t.Errorf("GetClientCertificate set = %v, want %v", hasGetClientCert, tc.wantGetClientCertificate)
			}
		})
	}
}

func TestWaitForFile(t *testing.T) {
	dir := t.TempDir()

	existing := filepath.Join(dir, "present")
	if err := os.WriteFile(existing, []byte("x"), fileMode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := WaitForFile(existing, shortTimeout); err != nil {
		t.Errorf("WaitForFile on an existing file: %v", err)
	}

	later := filepath.Join(dir, "later")
	go func() {
		time.Sleep(60 * time.Millisecond)
		_ = os.WriteFile(later, []byte("x"), fileMode)
	}()
	if err := WaitForFile(later, 5*time.Second); err != nil {
		t.Errorf("WaitForFile on a file created during the wait: %v", err)
	}
}

func TestWaitForFileTimesOut(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never")
	err := WaitForFile(missing, shortTimeout)
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want *TimeoutError", err)
	}
	if timeoutErr.Op != "WaitForFile" || timeoutErr.Target != missing {
		t.Errorf("unexpected error detail: %+v", timeoutErr)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Error("error does not unwrap to the last stat failure")
	}
}

func TestWaitForNodeHost(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "nodehost"), dirMode); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "nodehost", "host.id"), []byte("id"), fileMode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := WaitForNodeHost(dataDir, shortTimeout); err != nil {
		t.Errorf("WaitForNodeHost: %v", err)
	}
}

func TestWaitForNodeHostTimesOut(t *testing.T) {
	dataDir := t.TempDir()
	err := WaitForNodeHost(dataDir, shortTimeout)
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want *TimeoutError", err)
	}
	if !strings.Contains(timeoutErr.Target, filepath.Join("nodehost", "host.id")) {
		t.Errorf("Target = %q, want the nodehost host.id path", timeoutErr.Target)
	}
}

// TestWaitForNodeHostIgnoresEmptyHostID pins that a host.id which exists but
// holds no id yet does not count as the host being up: go-quicraft writes it
// with os.WriteFile, which creates the file before it writes the id, so a
// poll in between sees an empty file.
func TestWaitForNodeHostIgnoresEmptyHostID(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "nodehost"), dirMode); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "nodehost", "host.id"), nil, fileMode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	err := WaitForNodeHost(dataDir, shortTimeout)
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want *TimeoutError for an empty host.id", err)
	}
	if !errors.Is(err, errEmptyFile) {
		t.Errorf("err = %v, want it to unwrap to errEmptyFile", err)
	}
}

func TestWaitForHTTPOK(t *testing.T) {
	var requests atomic.Int32
	srv, pool := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	if err := WaitForHTTPOK(srv.URL+"/health", pool, nil, 10*time.Second); err != nil {
		t.Fatalf("WaitForHTTPOK: %v", err)
	}
	if got := requests.Load(); got < 3 {
		t.Errorf("server saw %d requests, want the waiter to have retried", got)
	}
}

func TestWaitForHTTPOKTimesOutOnBadStatus(t *testing.T) {
	srv, pool := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	})

	err := WaitForHTTPOK(srv.URL+"/health", pool, nil, shortTimeout)
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want *TimeoutError", err)
	}
	var probeErr *ProbeError
	if !errors.As(err, &probeErr) {
		t.Fatalf("err does not wrap a *ProbeError: %v", err)
	}
	if probeErr.Status != http.StatusInternalServerError {
		t.Errorf("Status = %d, want 500", probeErr.Status)
	}
	if probeErr.Body != "boom" {
		t.Errorf("Body = %q, want boom", probeErr.Body)
	}
}

func TestWaitForHTTPOKTimesOutWhenUnreachable(t *testing.T) {
	srv, pool := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	url := srv.URL + "/health"
	srv.Close()

	err := WaitForHTTPOK(url, pool, nil, shortTimeout)
	var probeErr *ProbeError
	if !errors.As(err, &probeErr) {
		t.Fatalf("err = %v, want a wrapped *ProbeError", err)
	}
	if !strings.Contains(probeErr.Reason, "request failed") {
		t.Errorf("Reason = %q, want a request failure", probeErr.Reason)
	}
}

func TestWaitForHTTPUpAcceptsOKAndTheSealedRefusal(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"ok": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
		"sealed": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"barrier sealed","code":"SEALED"}`))
		},
	} {
		srv, pool := newTLSServer(t, handler)
		if err := WaitForHTTPUp(srv.URL+"/health", pool, nil, 10*time.Second); err != nil {
			t.Errorf("%s: WaitForHTTPUp: %v", name, err)
		}
	}
}

func TestWaitForHTTPUpTimesOutOnAnUntypedUnavailable(t *testing.T) {
	srv, pool := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"starting"}`))
	})

	err := WaitForHTTPUp(srv.URL+"/health", pool, nil, shortTimeout)
	var probeErr *ProbeError
	if !errors.As(err, &probeErr) || probeErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want a timeout wrapping the 503 probe", err)
	}
}

func TestIsSealedRefusal(t *testing.T) {
	sealed := []byte(`{"error":"barrier sealed","code":"SEALED"}`)
	if !IsSealedRefusal(http.StatusServiceUnavailable, sealed) {
		t.Fatal("the sealed refusal is not recognised")
	}
	for name, c := range map[string]struct {
		status int
		body   []byte
	}{
		"other status": {http.StatusInternalServerError, sealed},
		"no code":      {http.StatusServiceUnavailable, []byte(`{"error":"starting"}`)},
		"not json":     {http.StatusServiceUnavailable, []byte("SEALED")},
	} {
		if IsSealedRefusal(c.status, c.body) {
			t.Errorf("%s: recognised as sealed", name)
		}
	}
}

func TestWaitForLeader(t *testing.T) {
	var requests atomic.Int32
	srv, pool := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if requests.Add(1) < 2 {
			_, _ = w.Write([]byte(`{"shards":[{"shard_id":1,"is_leader":false}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"shards":[{"shard_id":1,"is_leader":false},{"shard_id":2,"is_leader":true}]}`))
	})

	if err := WaitForLeader(srv.URL+"/status", pool, nil, 10*time.Second); err != nil {
		t.Fatalf("WaitForLeader: %v", err)
	}
	if got := requests.Load(); got < 2 {
		t.Errorf("server saw %d requests, want the waiter to have retried", got)
	}
}

func TestWaitForLeaderTimesOut(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantReason string
	}{
		{name: "no leader", status: http.StatusOK, body: `{"shards":[{"is_leader":false}]}`, wantReason: "no shard reports is_leader"},
		{name: "no shards", status: http.StatusOK, body: `{}`, wantReason: "no shard reports is_leader"},
		{name: "malformed", status: http.StatusOK, body: `not json`, wantReason: "decode"},
		{name: "forbidden", status: http.StatusForbidden, body: `denied`, wantReason: "unexpected status"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, pool := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})

			err := WaitForLeader(srv.URL+"/status", pool, nil, shortTimeout)
			var timeoutErr *TimeoutError
			if !errors.As(err, &timeoutErr) {
				t.Fatalf("err = %v, want *TimeoutError", err)
			}
			if timeoutErr.Op != "WaitForLeader" {
				t.Errorf("Op = %q, want WaitForLeader", timeoutErr.Op)
			}
			var probeErr *ProbeError
			if !errors.As(err, &probeErr) {
				t.Fatalf("err does not wrap a *ProbeError: %v", err)
			}
			if !strings.Contains(probeErr.Reason, tc.wantReason) {
				t.Errorf("Reason = %q, want it to contain %q", probeErr.Reason, tc.wantReason)
			}
		})
	}
}

func TestWaitForLeaderReportsUnreachableServer(t *testing.T) {
	srv, pool := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	url := srv.URL + "/status"
	srv.Close()

	err := WaitForLeader(url, pool, nil, shortTimeout)
	var probeErr *ProbeError
	if !errors.As(err, &probeErr) {
		t.Fatalf("err = %v, want a wrapped *ProbeError", err)
	}
	if !strings.Contains(probeErr.Reason, "request failed") {
		t.Errorf("Reason = %q, want a request failure", probeErr.Reason)
	}
}

func TestSleepUntil(t *testing.T) {
	if sleepUntil(time.Now().Add(-time.Second), time.Millisecond) {
		t.Error("sleepUntil returned true for a deadline in the past")
	}
	if !sleepUntil(time.Now().Add(time.Second), time.Millisecond) {
		t.Error("sleepUntil returned false for a deadline in the future")
	}
	if sleepUntil(time.Now().Add(5*time.Millisecond), 40*time.Millisecond) {
		t.Error("sleepUntil returned true after sleeping past the deadline")
	}
}
