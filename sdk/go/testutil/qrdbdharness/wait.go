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
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
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
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		_, err := os.Stat(path)
		if err == nil {
			return nil
		}
		lastErr = err
		if !sleepUntil(deadline, pollInterval) {
			return &TimeoutError{Op: "WaitForFile", Target: path, Timeout: timeout, LastErr: lastErr}
		}
	}
}

// WaitForNodeHost polls for the host.id file qrdbd writes under dataDir, which
// is the first evidence that the Raft node host came up. It is the cheapest
// possible liveness signal and needs no TLS material.
func WaitForNodeHost(dataDir string, timeout time.Duration) error {
	return WaitForFile(filepath.Join(dataDir, filepath.FromSlash(hostIDPath)), timeout)
}

// WaitForHTTPOK polls url until it answers 200, optionally presenting cert.
func WaitForHTTPOK(url string, pool *x509.CertPool, cert *tls.Certificate, timeout time.Duration) error {
	client := HTTPSClient(pool, cert, probeTimeout)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		lastErr = probeOK(client, url)
		if lastErr == nil {
			return nil
		}
		if !sleepUntil(deadline, pollInterval) {
			return &TimeoutError{Op: "WaitForHTTPOK", Target: url, Timeout: timeout, LastErr: lastErr}
		}
	}
}

// WaitForLeader polls statusURL — an authenticated /status endpoint, so cert
// must be an identity the daemon authorizes — until some shard reports itself
// leader. A single-node cluster self-elects almost immediately; this confirms
// Raft has caught up before ReadIndex-dependent operations are issued.
func WaitForLeader(statusURL string, pool *x509.CertPool, cert *tls.Certificate, timeout time.Duration) error {
	client := HTTPSClient(pool, cert, leaderProbeTimeout)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		lastErr = probeLeader(client, statusURL)
		if lastErr == nil {
			return nil
		}
		if !sleepUntil(deadline, leaderPollInterval) {
			return &TimeoutError{Op: "WaitForLeader", Target: statusURL, Timeout: timeout, LastErr: lastErr}
		}
	}
}

// sleepUntil sleeps for interval and reports whether the deadline still lies
// ahead, so callers get one probe attempt before the first sleep and one
// final attempt at the deadline.
func sleepUntil(deadline time.Time, interval time.Duration) bool {
	if !time.Now().Before(deadline) {
		return false
	}
	time.Sleep(interval)
	return time.Now().Before(deadline)
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
