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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
)

const (
	// pollInterval is how often the readiness waiters re-probe.
	pollInterval = 50 * time.Millisecond

	// leaderPollInterval is how often WaitForLeader re-probes. Leader election
	// is slower than process startup, so it polls less aggressively.
	leaderPollInterval = 100 * time.Millisecond

	// probeTimeout bounds a single readiness HTTP request.
	probeTimeout = 2 * time.Second

	// leaderProbeTimeout bounds a single /status request, which does more work
	// than /health.
	leaderProbeTimeout = 5 * time.Second

	// hostIDPath is the file qrdbd writes under its data directory once the
	// Raft node host has initialized.
	hostIDPath = "nodehost/host.id"

	// probeBodyLimit caps how much of a failing response is kept for
	// diagnostics.
	probeBodyLimit = 4096
)

// HTTPSClient returns an HTTP client that verifies the daemon's certificate
// against pool and, when cert is non-nil, authenticates with it over mTLS.
// A nil cert yields a client that can only reach public endpoints such as
// /health.
func HTTPSClient(pool *x509.CertPool, cert *tls.Certificate, timeout time.Duration) *http.Client {
	tlsConfig := &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS13,
	}
	if cert != nil {
		// GetClientCertificate, not Certificates: crypto/tls's default
		// getClientCertificate only offers a Certificates entry whose
		// issuer appears in the server's CertificateRequest.AcceptableCAs,
		// and silently sends NO certificate otherwise -- a server composed
		// over pkg/clienttrust.Pool (go-qrdb#591's review, B2) deliberately
		// advertises only its static baseline there, never a tenant's own
		// Key B CA, so a harness client presenting one would otherwise be
		// refused with no_peer_cert despite holding a certificate the
		// server's VerifyConnection (out-of-band from AcceptableCAs) would
		// actually trust. See sdk/go/transport.BuildTLSConfig's identical
		// fix for the SDK's own transports.
		tlsConfig.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return cert, nil
		}
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig:   tlsConfig,
			ForceAttemptHTTP2: true,
		},
	}
}

// WaitForFile polls until path exists or the timeout expires.
func WaitForFile(path string, timeout time.Duration) error {
	return waitForFile(nil, path, timeout)
}

// WaitForFile is the package-level WaitForFile that fails at once, with the
// daemon's output tail, if the daemon exits first.
func (d *Daemon) WaitForFile(path string, timeout time.Duration) error {
	return waitForFile(d, path, timeout)
}

func waitForFile(d *Daemon, path string, timeout time.Duration) error {
	return poll{
		op: "WaitForFile", target: path, timeout: timeout, interval: pollInterval,
		probe: func() error {
			_, err := os.Stat(path)
			return err
		},
	}.run(d)
}

// errEmptyFile reports a file that exists but holds no bytes yet.
var errEmptyFile = errors.New("file is empty")

// WaitForNodeHost polls for the host.id file qrdbd writes under dataDir, which
// is the first evidence that the Raft node host came up. It is the cheapest
// possible liveness signal and needs no TLS material. The file counts only once
// it holds the id: go-quicraft writes it with os.WriteFile, which creates the
// file before it writes the id, so a bare existence check can see it empty.
func WaitForNodeHost(dataDir string, timeout time.Duration) error {
	return waitForNodeHost(nil, dataDir, timeout)
}

// WaitForNodeHost is the package-level WaitForNodeHost that fails at once,
// with the daemon's output tail, if the daemon exits first.
func (d *Daemon) WaitForNodeHost(dataDir string, timeout time.Duration) error {
	return waitForNodeHost(d, dataDir, timeout)
}

func waitForNodeHost(d *Daemon, dataDir string, timeout time.Duration) error {
	path := filepath.Join(dataDir, filepath.FromSlash(hostIDPath))
	return poll{
		op: "WaitForNodeHost", target: path, timeout: timeout, interval: pollInterval,
		probe: func() error {
			info, err := os.Stat(path)
			if err == nil && info.Size() == 0 {
				return errEmptyFile
			}
			return err
		},
	}.run(d)
}

// WaitForHTTPOK polls url until it answers 200, optionally presenting cert.
//
// On a timeout it sends SIGQUIT to any daemon this package started whose
// arguments name url's port, and waits for that daemon's goroutine dump to
// reach its output before returning, so a node that stalled during startup
// names the call it is blocked in. The signalled daemon exits; the returned
// *TimeoutError lists its process ID in StackDumped.
func WaitForHTTPOK(url string, pool *x509.CertPool, cert *tls.Certificate, timeout time.Duration) error {
	return waitForHTTPOK(nil, url, pool, cert, timeout)
}

// WaitForHTTPOK is the package-level WaitForHTTPOK that fails at once, with
// the daemon's output tail, if the daemon exits first.
func (d *Daemon) WaitForHTTPOK(url string, pool *x509.CertPool, cert *tls.Certificate, timeout time.Duration) error {
	return waitForHTTPOK(d, url, pool, cert, timeout)
}

func waitForHTTPOK(d *Daemon, url string, pool *x509.CertPool, cert *tls.Certificate, timeout time.Duration) error {
	client := HTTPSClient(pool, cert, probeTimeout)
	return poll{
		op: "WaitForHTTPOK", target: url, timeout: timeout, interval: pollInterval,
		probe:     func() error { return probeOK(client, url) },
		onTimeout: func() []int { return dumpStacks(url) },
	}.run(d)
}

// WaitForHTTPUp polls url until the node answers it with a 200 or with the
// typed sealed refusal (503, code SEALED), or timeout elapses. A sealed node
// serves the unseal call and nothing else, so the sealed refusal on any other
// route is how it says it is up and sealed; a 503 without the SEALED code (a
// node still starting) keeps the wait going.
func WaitForHTTPUp(url string, pool *x509.CertPool, cert *tls.Certificate, timeout time.Duration) error {
	return waitForHTTPUp(nil, url, pool, cert, timeout)
}

// WaitForHTTPUp is the package-level WaitForHTTPUp that fails at once, with
// the daemon's output tail, if the daemon exits first.
func (d *Daemon) WaitForHTTPUp(url string, pool *x509.CertPool, cert *tls.Certificate, timeout time.Duration) error {
	return waitForHTTPUp(d, url, pool, cert, timeout)
}

func waitForHTTPUp(d *Daemon, url string, pool *x509.CertPool, cert *tls.Certificate, timeout time.Duration) error {
	client := HTTPSClient(pool, cert, probeTimeout)
	return poll{
		op: "WaitForHTTPUp", target: url, timeout: timeout, interval: pollInterval,
		probe: func() error { return probeUp(client, url) },
	}.run(d)
}

// WaitForLeader polls statusURL — an authenticated /status endpoint, so cert
// must be an identity the daemon authorizes — until some shard reports itself
// leader. A single-node cluster self-elects almost immediately; this confirms
// Raft has caught up before ReadIndex-dependent operations are issued.
func WaitForLeader(statusURL string, pool *x509.CertPool, cert *tls.Certificate, timeout time.Duration) error {
	return waitForLeader(nil, statusURL, pool, cert, timeout)
}

// WaitForLeader is the package-level WaitForLeader that fails at once, with
// the daemon's output tail, if the daemon exits first.
func (d *Daemon) WaitForLeader(statusURL string, pool *x509.CertPool, cert *tls.Certificate, timeout time.Duration) error {
	return waitForLeader(d, statusURL, pool, cert, timeout)
}

func waitForLeader(d *Daemon, statusURL string, pool *x509.CertPool, cert *tls.Certificate, timeout time.Duration) error {
	client := HTTPSClient(pool, cert, leaderProbeTimeout)
	return poll{
		op: "WaitForLeader", target: statusURL, timeout: timeout, interval: leaderPollInterval,
		probe: func() error { return probeLeader(client, statusURL) },
	}.run(d)
}

// poll is one readiness wait: probe runs until it succeeds or timeout
// elapses, with interval between attempts.
type poll struct {
	op       string
	target   string
	timeout  time.Duration
	interval time.Duration
	probe    func() error
	// onTimeout, when set, runs as the wait gives up and returns the daemons
	// it sent SIGQUIT, for TimeoutError.StackDumped.
	onTimeout func() []int
}

// run polls p.probe, with one probe attempt before the first sleep. With a
// non-nil d it also watches the daemon and, the moment it exits, returns
// d.exitError rather than polling a process that is gone until the timeout.
// A nil d waits on a nil channel, which never fires.
func (p poll) run(d *Daemon) error {
	var exited <-chan struct{}
	if d != nil {
		exited = d.Exited()
	}
	deadline := time.Now().Add(p.timeout)
	for {
		lastErr := p.probe()
		if lastErr == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			return p.timeoutError(lastErr)
		}
		timer := time.NewTimer(p.interval)
		select {
		case <-exited:
			timer.Stop()
			return d.exitError(p.op, p.target)
		case <-timer.C:
		}
		if !time.Now().Before(deadline) {
			return p.timeoutError(lastErr)
		}
	}
}

func (p poll) timeoutError(lastErr error) *TimeoutError {
	err := &TimeoutError{Op: p.op, Target: p.target, Timeout: p.timeout, LastErr: lastErr}
	if p.onTimeout != nil {
		err.StackDumped = p.onTimeout()
	}
	return err
}

// probeOK issues one request and reports why it was not a 200, if it was not.
func probeOK(client *http.Client, url string) error {
	resp, err := client.Get(url)
	if err != nil {
		return &ProbeError{Reason: "request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, probeBodyLimit))
	if resp.StatusCode != http.StatusOK {
		return &ProbeError{Reason: "unexpected status", Status: resp.StatusCode, Body: string(body)}
	}
	return nil
}

// probeUp returns nil when url answers 200 or the typed sealed refusal.
func probeUp(client *http.Client, url string) error {
	resp, err := client.Get(url)
	if err != nil {
		return &ProbeError{Reason: "request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, probeBodyLimit))
	if resp.StatusCode == http.StatusOK || IsSealedRefusal(resp.StatusCode, body) {
		return nil
	}
	return &ProbeError{Reason: "unexpected status", Status: resp.StatusCode, Body: string(body)}
}

// IsSealedRefusal reports whether an HTTP answer is a sealed node's typed
// refusal: 503 with the SEALED code.
func IsSealedRefusal(status int, body []byte) bool {
	if status != http.StatusServiceUnavailable {
		return false
	}
	var refusal struct {
		Code string `json:"code"`
	}
	return json.Unmarshal(body, &refusal) == nil && refusal.Code == sealedwindow.ReasonSealed
}

// statusResponse is the subset of qrdbd's /status payload the harness reads.
type statusResponse struct {
	Shards []struct {
		IsLeader bool `json:"is_leader"`
	} `json:"shards"`
}

// probeLeader issues one /status request and reports why no leader was seen.
func probeLeader(client *http.Client, statusURL string) error {
	resp, err := client.Get(statusURL)
	if err != nil {
		return &ProbeError{Reason: "request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, probeBodyLimit))
	if err != nil {
		return &ProbeError{Reason: "read body: " + err.Error()}
	}
	if resp.StatusCode != http.StatusOK {
		return &ProbeError{Reason: "unexpected status", Status: resp.StatusCode, Body: string(body)}
	}
	var status statusResponse
	if err := json.Unmarshal(body, &status); err != nil {
		return &ProbeError{Reason: "decode: " + err.Error(), Body: string(body)}
	}
	for _, shard := range status.Shards {
		if shard.IsLeader {
			return nil
		}
	}
	return &ProbeError{Reason: "no shard reports is_leader"}
}
