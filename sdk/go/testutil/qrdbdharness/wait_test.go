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

func TestPollRunTimesOutPastTheDeadline(t *testing.T) {
	probeErr := errors.New("not yet")
	var probes atomic.Int32
	err := poll{
		op: "op", target: "target", timeout: 0, interval: time.Hour,
		probe: func() error { probes.Add(1); return probeErr },
	}.run(nil)
	var timeout *TimeoutError
	if !errors.As(err, &timeout) || timeout.Op != "op" || timeout.Target != "target" {
		t.Fatalf("err = %v, want a *TimeoutError for op on target", err)
	}
	if !errors.Is(timeout.LastErr, probeErr) {
		t.Errorf("LastErr = %v, want the probe's error", timeout.LastErr)
	}
	if got := probes.Load(); got != 1 {
		t.Errorf("probes = %d, want exactly one attempt before giving up", got)
	}
}

func TestPollRunReportsStackDumpsOnTimeout(t *testing.T) {
	err := poll{
		op: "op", target: "target", timeout: time.Millisecond, interval: 2 * time.Millisecond,
		probe:     func() error { return errEmptyFile },
		onTimeout: func() []int { return []int{42} },
	}.run(nil)
	var timeout *TimeoutError
	if !errors.As(err, &timeout) || len(timeout.StackDumped) != 1 || timeout.StackDumped[0] != 42 {
		t.Fatalf("err = %v, want a *TimeoutError listing the dumped daemon", err)
	}
}

func TestPollRunRetriesUntilTheProbeSucceeds(t *testing.T) {
	var probes atomic.Int32
	err := poll{
		op: "op", target: "target", timeout: 10 * time.Second, interval: time.Millisecond,
		probe: func() error {
			if probes.Add(1) < 3 {
				return errEmptyFile
			}
			return nil
		},
	}.run(nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
}

// neverReadyServer answers every probe with a 503 that is not the sealed
// refusal and a status with no leader, so no HTTP wait on it ever succeeds.
func neverReadyServer(t *testing.T) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	return newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"shards":[{"shard_id":1,"is_leader":false}]}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
}

// daemonWaits names every Daemon readiness wait, bound to a target it never
// reaches.
func daemonWaits(t *testing.T) map[string]func(*Daemon, time.Duration) error {
	t.Helper()
	srv, pool := neverReadyServer(t)
	missing := filepath.Join(t.TempDir(), "missing")
	return map[string]func(*Daemon, time.Duration) error{
		"WaitForFile":     func(d *Daemon, to time.Duration) error { return d.WaitForFile(missing, to) },
		"WaitForNodeHost": func(d *Daemon, to time.Duration) error { return d.WaitForNodeHost(missing, to) },
		"WaitForHTTPOK":   func(d *Daemon, to time.Duration) error { return d.WaitForHTTPOK(srv.URL+"/health", pool, nil, to) },
		"WaitForHTTPUp":   func(d *Daemon, to time.Duration) error { return d.WaitForHTTPUp(srv.URL+"/health", pool, nil, to) },
		"WaitForLeader":   func(d *Daemon, to time.Duration) error { return d.WaitForLeader(srv.URL+"/status", pool, nil, to) },
	}
}

func TestDaemonWaitFailsAtOnceWhenTheDaemonExits(t *testing.T) {
	const timeout = 5 * time.Minute
	for name, wait := range daemonWaits(t) {
		t.Run(name, func(t *testing.T) {
			d := startHelper(t, "print")
			start := time.Now()
			err := wait(d, timeout)
			if elapsed := time.Since(start); elapsed > timeout/10 {
				t.Errorf("wait took %v, want it to end at the exit, well under %v", elapsed, timeout)
			}
			var waitErr *DaemonWaitError
			if !errors.As(err, &waitErr) {
				t.Fatalf("err = %v, want a *DaemonWaitError", err)
			}
			var exit *DaemonExitError
			if !errors.As(err, &exit) || exit.Op != name {
				t.Fatalf("err = %v, want a *DaemonExitError for %s", err, name)
			}
			if !strings.Contains(waitErr.Tail, "problem") || !strings.Contains(err.Error(), " exited: ") {
				t.Errorf("err = %v, want the exit and the daemon's output tail", err)
			}
			var collision *PortCollisionError
			if errors.As(err, &collision) {
				t.Errorf("err = %v, want no port collision for a tail without one", err)
			}
		})
	}
}

func TestDaemonWaitReportsAFailingExitStatus(t *testing.T) {
	d := startHelper(t, "unknown-mode")
	err := d.WaitForFile(filepath.Join(t.TempDir(), "missing"), time.Minute)
	var exit *DaemonExitError
	if !errors.As(err, &exit) || exit.Status == nil {
		t.Fatalf("err = %v, want a *DaemonExitError carrying the exit status", err)
	}
	if !strings.Contains(err.Error(), exit.Status.Error()) {
		t.Errorf("err = %v, want it to name the exit status", err)
	}
}

func TestDaemonWaitReachesReadyOnALiveDaemon(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	if err := os.WriteFile(ready, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(filepath.Dir(ready), filepath.Dir(filepath.FromSlash(hostIDPath))), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(ready), filepath.FromSlash(hostIDPath)), []byte("id"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, pool := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"shards":[{"shard_id":1,"is_leader":true}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	d := startHelper(t, "sleep")
	waits := map[string]func() error{
		"WaitForFile":     func() error { return d.WaitForFile(ready, 10*time.Second) },
		"WaitForNodeHost": func() error { return d.WaitForNodeHost(filepath.Dir(ready), 10*time.Second) },
		"WaitForHTTPOK":   func() error { return d.WaitForHTTPOK(srv.URL+"/health", pool, nil, 10*time.Second) },
		"WaitForHTTPUp":   func() error { return d.WaitForHTTPUp(srv.URL+"/health", pool, nil, 10*time.Second) },
		"WaitForLeader":   func() error { return d.WaitForLeader(srv.URL+"/status", pool, nil, 10*time.Second) },
	}
	for name, wait := range waits {
		if err := wait(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDaemonWaitTimesOutOnALiveDaemon(t *testing.T) {
	d := startHelper(t, "sleep")
	err := d.WaitForFile(filepath.Join(t.TempDir(), "missing"), shortTimeout)
	var timeout *TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("err = %v, want a *TimeoutError while the daemon still runs", err)
	}
}
