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

//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

const (
	// testOrganization is the X.509 Organization on every certificate this
	// suite mints, so a stray certificate is attributable to these tests.
	testOrganization = "go-quicraft-test"

	// adminUser and adminTenant are the CN/OU the qrdb mTLS authenticator maps
	// to the identity firstboot.Initialize provisions: the admin user in the
	// reserved system tenant, which the system-tenant rule lifts to
	// platform-wide authority.
	adminUser   = "admin"
	adminTenant = "system"
)

// testCA is this suite's fatal-on-error face for the shared qrdbd harness
// CA. The certificate authority itself, and every certificate it mints, come
// from sdk/go/testutil/qrdbdharness so the GORM
// conformance suites mint identical material; only the *testing.T ergonomics
// are local.
type testCA struct {
	*qrdbdharness.CA

	// adminOnce guards the admin client certificate mtlsHTTPClient presents.
	// It is minted once per CA and reused: the HTTP helpers build a fresh
	// client per request, and minting a new key pair for every KV round trip
	// would dominate their cost.
	adminOnce sync.Once
	adminCert tls.Certificate
	adminErr  error
}

// generateTestCA creates an ephemeral ECDSA P-256 CA certificate for
// integration tests. The CA cert is written to a temp file and the path
// is available as testCA.CertPath.
func generateTestCA(t *testing.T) *testCA {
	t.Helper()
	ca, err := qrdbdharness.NewCA(t.TempDir(),
		qrdbdharness.WithCAOrganization(testOrganization),
		qrdbdharness.WithCACommonName("test-ca"),
	)
	if err != nil {
		t.Fatalf("generateTestCA: %v", err)
	}
	return &testCA{CA: ca}
}

// issue mints a certificate into dir or fails the test.
func (ca *testCA) issue(t *testing.T, dir string, opts ...qrdbdharness.CertOption) *qrdbdharness.KeyPair {
	t.Helper()
	opts = append([]qrdbdharness.CertOption{qrdbdharness.WithCertOrganization(testOrganization)}, opts...)
	kp, err := ca.Issue(dir, opts...)
	if err != nil {
		t.Fatalf("issue certificate: %v", err)
	}
	return kp
}

// generateNodeCert creates a leaf certificate signed by this CA with
// SANs for all local hostnames and interface IPs. The cert and key
// are written to temp files and their paths are returned.
func (ca *testCA) generateNodeCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	kp := ca.issue(t, t.TempDir(),
		qrdbdharness.WithSubject("test-node", ""),
		qrdbdharness.WithServerAuth(),
	)
	return kp.CertPath, kp.KeyPath
}

// generateAdminClientCert creates a client-only certificate signed by this
// CA with CN=admin and OU=system. This matches the admin user created by
// firstboot.Initialize so the mTLS authenticator recognizes it. The cert
// and key are written to {dataDir}/admin-cert.pem and admin-key.pem.
func (ca *testCA) generateAdminClientCert(t *testing.T, dataDir string) (certPath, keyPath string) {
	t.Helper()
	kp := ca.issue(t, dataDir, qrdbdharness.WithSubject(adminUser, adminTenant))
	return kp.CertPath, kp.KeyPath
}

// tlsFlags returns the CLI flags for TLS cert files for a single node.
func (ca *testCA) tlsFlags(t *testing.T) []string {
	t.Helper()
	kp := ca.issue(t, t.TempDir(),
		qrdbdharness.WithSubject("test-node", ""),
		qrdbdharness.WithServerAuth(),
	)
	return ca.TLSFlags(kp)
}

// caHTTPClient returns an HTTP client that verifies server certificates
// using the given CA certificate pool. This client does NOT present a
// client certificate, so it can only access public endpoints like /health.
func caHTTPClient(pool *x509.CertPool, timeout time.Duration) *http.Client {
	return qrdbdharness.HTTPSClient(pool, nil, timeout)
}

// mtlsHTTPClient returns an HTTP client that presents a client certificate
// signed by the given CA with CN=admin and OU=system. This matches the
// admin user created by firstboot.Initialize, so the mTLS authenticator
// + RBAC authorizer grants Admin@system platform-wide access via the
// system-tenant rule.
func mtlsHTTPClient(t *testing.T, ca *testCA, timeout time.Duration) *http.Client {
	t.Helper()
	ca.adminOnce.Do(func() {
		kp, err := ca.Issue(t.TempDir(),
			qrdbdharness.WithCertOrganization(testOrganization),
			qrdbdharness.WithSubject(adminUser, adminTenant),
		)
		if err != nil {
			ca.adminErr = err
			return
		}
		// Only the parsed certificate is kept, so the files may be reaped
		// with the temp directory of whichever test minted it first.
		ca.adminCert, ca.adminErr = kp.TLSCertificate()
	})
	if ca.adminErr != nil {
		t.Fatalf("mtlsHTTPClient: %v", ca.adminErr)
	}
	return qrdbdharness.HTTPSClient(ca.Pool, &ca.adminCert, timeout)
}

// buildBinary compiles the qrdbd daemon binary and returns its path. The
// harness builds it at most once per test run and caches it per tag set.
func buildBinary(t *testing.T) string {
	t.Helper()
	binary, err := qrdbdharness.BuildBinary(qrdbdharness.DaemonPackage)
	if err != nil {
		t.Fatalf("buildBinary: %v", err)
	}
	return binary
}

// buildCLIBinary compiles the qrdb client CLI binary (headless, no desktop
// GUI tags) and returns its path. It mirrors buildBinary but targets
// ./cmd/qrdb instead of the daemon, so integration tests can exercise the
// real client CLI (e.g. `qrdb admin drain`) against a spawned qrdbd node.
func buildCLIBinary(t *testing.T) string {
	t.Helper()
	binary, err := qrdbdharness.BuildBinary(qrdbdharness.ClientPackage,
		qrdbdharness.WithBuildTags("qrdb_ts qrdb_vector"))
	if err != nil {
		t.Fatalf("buildCLIBinary: %v", err)
	}
	return binary
}

// logWriter is an io.Writer that forwards lines to t.Log with a prefix.
// Line splitting and buffering belong to qrdbdharness.PrefixWriter; this type
// only adapts *testing.T to io.Writer and is initialized lazily because
// callers construct it as a struct literal.
type logWriter struct {
	t      *testing.T
	prefix string

	initOnce sync.Once
	prefixed *qrdbdharness.PrefixWriter
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.initOnce.Do(func() {
		w.prefixed = qrdbdharness.NewPrefixWriter(testLogSink{t: w.t}, w.prefix)
	})
	return w.prefixed.Write(p)
}

// testLogSink adapts *testing.T to io.Writer, trimming the newline that
// t.Log adds back itself.
type testLogSink struct {
	t *testing.T
}

func (s testLogSink) Write(p []byte) (int, error) {
	s.t.Log(strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}

// startNode starts the qrdbd binary with the given arguments and returns
// the exec.Cmd. Stdout and stderr are piped through t.Log with the given prefix.
// The process is killed on test cleanup.
//
// The raw exec.Cmd is returned rather than the harness Daemon because these
// tests drive process lifecycle themselves — SIGTERM restarts, crash
// simulations, and their own Wait calls — and must not share reaping with the
// harness.
func startNode(t *testing.T, binary string, args []string, prefix string) *exec.Cmd {
	t.Helper()
	daemon, err := qrdbdharness.Start(binary, args,
		qrdbdharness.WithOutput(&logWriter{t: t, prefix: prefix}))
	if err != nil {
		t.Fatalf("startNode: %v", err)
	}
	cmd := daemon.Cmd()
	t.Cleanup(func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	return cmd
}

// allocatePorts returns n free UDP ports for node listen addresses. The
// reserved-window allocator lives in sdk/go/testutil/qrdbdharness: ports are
// drawn from a window BELOW the kernel's ephemeral range and bind-verified
// before being returned, which is what keeps a daemon's listen port from
// being stolen as some other process's outbound source port.
func allocatePorts(t *testing.T, n int) []int {
	t.Helper()
	ports, err := qrdbdharness.ReserveUDPPorts(n)
	if err != nil {
		t.Fatalf("allocatePorts: %v", err)
	}
	return ports
}

// memberIdentitiesFlag builds the --member-identities value for a cluster whose
// replica i+1 presents the certificate at certPaths[i]: "1=spki:…,2=spki:…".
// It names every replica, the local one included — a node accepts its own
// entry when it matches its certificate — so every node can take the same
// value. go-quicraft admits a Raft member by this identity alone (#556).
func memberIdentitiesFlag(t *testing.T, certPaths []string) string {
	t.Helper()
	parts := make([]string, len(certPaths))
	for i, p := range certPaths {
		certPEM, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read node certificate %s: %v", p, err)
		}
		identity, err := cluster.MemberIdentityFromPEM(certPEM)
		if err != nil {
			t.Fatalf("member identity of %s: %v", p, err)
		}
		parts[i] = fmt.Sprintf("%d=%s", i+1, identity)
	}
	return strings.Join(parts, ",")
}

// clusterNodeTLS issues n node certificates up front — a node needs every
// peer's identity before it starts — and returns each node's TLS flags plus the
// --member-identities value naming all of them.
func (ca *testCA) clusterNodeTLS(t *testing.T, n int) ([][]string, string) {
	t.Helper()
	flags := make([][]string, n)
	certPaths := make([]string, n)
	for i := range n {
		kp := ca.issue(t, t.TempDir(),
			qrdbdharness.WithSubject("test-node", ""),
			qrdbdharness.WithServerAuth(),
		)
		flags[i] = ca.TLSFlags(kp)
		certPaths[i] = kp.CertPath
	}
	return flags, memberIdentitiesFlag(t, certPaths)
}

// buildPeersFlag builds the --peers flag value from ports.
// Format: "1=127.0.0.1:port1,2=127.0.0.1:port2,..."
func buildPeersFlag(ports []int) string {
	parts := make([]string, len(ports))
	for i, port := range ports {
		parts[i] = fmt.Sprintf("%d=127.0.0.1:%d", i+1, port)
	}
	return strings.Join(parts, ",")
}

// waitForHealthy polls for the host.id file in the data directory
// to indicate the node has started successfully.
func waitForHealthy(t *testing.T, dataDir string, timeout time.Duration) {
	t.Helper()
	if err := qrdbdharness.WaitForNodeHost(dataDir, timeout); err != nil {
		t.Fatal(err)
	}
}

// runCLI runs the binary with args and returns the combined output and error.
// A 30-second timeout prevents tests from hanging if the binary doesn't exit.
func runCLI(t *testing.T, binary string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// allocateTCPPorts returns n free TCP ports for server addresses. It shares
// the harness reservation table with allocatePorts, so a number handed out for
// UDP is never handed out for TCP.
func allocateTCPPorts(t *testing.T, n int) []int {
	t.Helper()
	ports, err := qrdbdharness.ReserveTCPPorts(n)
	if err != nil {
		t.Fatalf("allocateTCPPorts: %v", err)
	}
	return ports
}

// waitForAdmin polls the admin server's /health endpoint until it returns
// a 200 or the timeout is reached.
func waitForAdmin(t *testing.T, pool *x509.CertPool, port int, timeout time.Duration) {
	t.Helper()
	if err := qrdbdharness.WaitForHTTPOK(adminURL(port, "/health"), pool, nil, timeout); err != nil {
		t.Fatal(err)
	}
}

// adminURL builds an admin API URL for a node on loopback.
func adminURL(port int, path string) string {
	return fmt.Sprintf("https://127.0.0.1:%d%s", port, path)
}

// httpPut sends a PUT request to the admin server's KV endpoint.
// Retries up to 3 times on transient errors (timeouts, connection refused).
func httpPut(t *testing.T, ca *testCA, port int, key string, value []byte) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(value))
		if err != nil {
			t.Fatalf("httpPut: new request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpPut %s: status %d, body: %s", key, resp.StatusCode, body)
		}
		return
	}
	t.Fatalf("httpPut: %v (after %d attempts)", lastErr, maxRetries)
}

// httpGet sends a GET request to the admin server's KV endpoint and returns the value.
// Retries up to 3 times on transient errors (timeouts, connection refused).
func httpGet(t *testing.T, ca *testCA, port int, key string) []byte {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpGet %s: status %d, body: %s", key, resp.StatusCode, body)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("httpGet: read body: %v", err)
		}
		return body
	}
	t.Fatalf("httpGet: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// httpGetStale sends a GET request with ?stale=true for local state machine
// reads without Raft coordination. Retries up to 3 times on transient errors.
func httpGetStale(t *testing.T, ca *testCA, port int, key string) []byte {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s?stale=true", port, key)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpGetStale %s: status %d, body: %s", key, resp.StatusCode, body)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("httpGetStale: read body: %v", err)
		}
		return body
	}
	t.Fatalf("httpGetStale: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// httpGetStatus sends a GET request and returns status code (for testing 404 etc).
// Retries up to 3 times on transient errors (timeouts, connection refused).
func httpGetStatus(t *testing.T, ca *testCA, port int, key string) int {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		io.ReadAll(resp.Body)
		return resp.StatusCode
	}
	t.Fatalf("httpGetStatus: %v (after %d attempts)", lastErr, maxRetries)
	return 0
}

// httpDelete sends a DELETE request to the admin server's KV endpoint.
// httpDelete sends a DELETE request to the admin server's KV endpoint.
// Retries up to 3 times on transient errors (timeouts, connection refused).
func httpDelete(t *testing.T, ca *testCA, port int, key string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		req, err := http.NewRequest(http.MethodDelete, url, nil)
		if err != nil {
			t.Fatalf("httpDelete: new request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpDelete %s: status %d, body: %s", key, resp.StatusCode, body)
		}
		return
	}
	t.Fatalf("httpDelete: %v (after %d attempts)", lastErr, maxRetries)
}

// httpList sends a GET request to list KV keys by prefix.
// Retries up to 3 times on transient errors (timeouts, connection refused).
func httpList(t *testing.T, ca *testCA, port int, prefix string) []string {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv?prefix=%s", port, prefix)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpList prefix=%s: status %d, body: %s", prefix, resp.StatusCode, body)
		}
		var result struct {
			Keys []string `json:"keys"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("httpList: decode: %v", err)
		}
		return result.Keys
	}
	t.Fatalf("httpList: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// httpStatus fetches the /status endpoint and returns the decoded response.
func httpStatus(t *testing.T, ca *testCA, port int) map[string]interface{} {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/status", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("httpStatus: %v", err)
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("httpStatus: decode: %v", err)
	}
	return result
}

// httpPeers issues a GET /peers request and decodes the response into a
// generic map for inspection (shard_id, leader_id, and per-shard peer lists
// with replica_id/address/is_leader).
func httpPeers(t *testing.T, ca *testCA, port int) map[string]interface{} {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/peers", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("httpPeers: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("httpPeers: status %d, body: %s", resp.StatusCode, body)
	}
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("httpPeers: decode: %v", err)
	}
	return result
}

// httpMembershipAdd issues a POST /membership/add request to the given
// admin port and returns the HTTP status code and raw response body. Unlike
// httpPut/httpGet, it does not assert on the outcome -- callers exercising
// leader-forwarding behavior need to inspect the status code themselves.
func httpMembershipAdd(t *testing.T, ca *testCA, port int, shardID, replicaID uint64, address string) (int, string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/membership/add", port)
	client := mtlsHTTPClient(t, ca, 15*time.Second)
	payload := map[string]interface{}{
		"shard_id":   shardID,
		"replica_id": replicaID,
		"address":    address,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpMembershipAdd: marshal: %v", err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("httpMembershipAdd: %v", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("httpMembershipAdd: read body: %v", err)
	}
	return resp.StatusCode, string(respBody)
}

// httpMembershipRemove issues a POST /membership/remove request to the given
// admin port and returns the HTTP status code and raw response body. Like
// httpMembershipAdd, it does not assert on the outcome -- callers exercising
// leader-forwarding behavior need to inspect the status code themselves.
func httpMembershipRemove(t *testing.T, ca *testCA, port int, shardID, replicaID uint64) (int, string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/membership/remove", port)
	client := mtlsHTTPClient(t, ca, 15*time.Second)
	payload := map[string]interface{}{
		"shard_id":   shardID,
		"replica_id": replicaID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpMembershipRemove: marshal: %v", err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("httpMembershipRemove: %v", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("httpMembershipRemove: read body: %v", err)
	}
	return resp.StatusCode, string(respBody)
}

// httpHealth returns the HTTP status code from /health.
func httpHealth(t *testing.T, pool *x509.CertPool, port int) int {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/health", port)
	client := caHTTPClient(pool, 10*time.Second)
	resp, err := client.Get(url)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)
	return resp.StatusCode
}

// waitForLeaderViaAdmin polls all nodes' /status endpoint until at least one
// reports is_leader:true for shard 1.
func waitForLeaderViaAdmin(t *testing.T, ca *testCA, ports []int, timeout time.Duration) int {
	t.Helper()
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	deadline := time.Now().Add(timeout)
	var lastDiag string
	for time.Now().Before(deadline) {
		for _, port := range ports {
			status, diag := httpStatusRaw(t, client, port)
			if diag != "" {
				lastDiag = fmt.Sprintf("port %d: %s", port, diag)
			}
			if status != nil {
				if shards, ok := status["shards"].([]interface{}); ok {
					for _, s := range shards {
						shard, ok := s.(map[string]interface{})
						if !ok {
							continue
						}
						if isLeader, ok := shard["is_leader"].(bool); ok && isLeader {
							return port
						}
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for leader via admin (last: %s)", timeout, lastDiag)
	return 0
}

// httpStatusRaw fetches /status without failing the test. Returns nil on error
// and a diagnostic string describing the last failure.
func httpStatusRaw(t *testing.T, client *http.Client, port int) (map[string]interface{}, string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/status", port)
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Sprintf("GET error: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Sprintf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Sprintf("status %d: %s", resp.StatusCode, string(body))
	}
	var result map[string]interface{}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&result); err != nil {
		return nil, fmt.Sprintf("decode: %v", err)
	}
	return result, ""
}

// httpListTolerant sends a GET request to list KV keys by prefix. Returns nil
// on any error instead of fataling, allowing callers to retry.
func httpListTolerant(t *testing.T, ca *testCA, port int, prefix string) []string {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv?prefix=%s", port, prefix)
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	resp, err := client.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.ReadAll(resp.Body)
		return nil
	}
	var result struct {
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}
	return result.Keys
}

// warmupWrite performs a PUT followed by a DELETE to force the leader to commit
// entries at the current term (including its no-op entry). This ensures the
// ReadIndex pipeline is operational before tests issue reads. The key is deleted
// so it doesn't pollute test assertions on key counts.
func warmupWrite(t *testing.T, ca *testCA, port int, timeout time.Duration) {
	t.Helper()
	const warmupKey = "__warmup__"
	deadline := time.Now().Add(timeout)
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	var lastStatus int
	var lastErr error
	var lastBody string
	for time.Now().Before(deadline) {
		url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, warmupKey)
		req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader([]byte("1")))
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastStatus = resp.StatusCode
		lastBody = string(body)
		lastErr = nil
		if resp.StatusCode == http.StatusNoContent {
			// Clean up the warmup key.
			delReq, _ := http.NewRequest(http.MethodDelete, url, nil)
			delResp, delErr := client.Do(delReq)
			if delErr == nil {
				delResp.Body.Close()
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("timed out after %v waiting for warmup write on port %d: last error: %v", timeout, port, lastErr)
	}
	t.Fatalf("timed out after %v waiting for warmup write on port %d: last status %d, body: %s", timeout, port, lastStatus, lastBody)
}

// waitForKV polls GET /kv/{key} until the value matches expected or timeout.
func waitForKV(t *testing.T, ca *testCA, port int, key string, expected []byte, timeout time.Duration) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	deadline := time.Now().Add(timeout)
	client := mtlsHTTPClient(t, ca, 2*time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && bytes.Equal(body, expected) {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for key %q to equal %q", timeout, key, expected)
}

// ---------------------------------------------------------------------------
// Batch helpers
// ---------------------------------------------------------------------------

// batchOp is the JSON representation of an operation in a batch or transaction.
type batchOp struct {
	Type     string `json:"type"`
	Key      string `json:"key"`
	Value    string `json:"value,omitempty"`
	RangeEnd string `json:"range_end,omitempty"`
	LeaseID  int64  `json:"lease_id,omitempty"`
}

// batchOpResponse is the JSON representation of an operation result.
type batchOpResponse struct {
	Kvs       []kvResult `json:"kvs,omitempty"`
	Count     int64      `json:"count,omitempty"`
	Succeeded bool       `json:"succeeded"`
}

// batchResult is the JSON payload returned by POST /batch.
type batchResult struct {
	Responses []batchOpResponse `json:"responses"`
}

// kvResult is the JSON representation of an MVCC key-value pair. Key and
// Value are []byte because that is the REST contract: kvResponse in
// pkg/api/rest encodes both through encoding/json's standard base64 for
// []byte so arbitrary byte-valued keys round-trip exactly, and the SDK's
// REST transport (sdk/go/transport/rest/transport_mvcc.go) decodes the
// same way. Compare with string(kv.Key) at the assertion site.
type kvResult struct {
	Key            []byte `json:"key"`
	Value          []byte `json:"value"`
	CreateRevision int64  `json:"create_revision"`
	ModRevision    int64  `json:"mod_revision"`
	Version        int64  `json:"version"`
	LeaseID        int64  `json:"lease_id"`
}

// httpBatch sends a POST request to /batch with the given operations and
// returns the batch result. Retries up to 3 times on transient errors.
func httpBatch(t *testing.T, ca *testCA, port int, ops []batchOp) batchResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/batch", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Ops []batchOp `json:"ops"`
	}{Ops: ops}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpBatch: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpBatch: status %d, body: %s", resp.StatusCode, respBody)
		}
		var result batchResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpBatch: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpBatch: %v (after %d attempts)", lastErr, maxRetries)
	return batchResult{}
}

// ---------------------------------------------------------------------------
// Transaction helpers
// ---------------------------------------------------------------------------

// txCompare is the JSON representation of a transaction compare precondition.
type txCompare struct {
	Key            string `json:"key"`
	Target         string `json:"target"`
	Result         string `json:"result"`
	Value          string `json:"value,omitempty"`
	Version        int64  `json:"version,omitempty"`
	CreateRevision int64  `json:"create_revision,omitempty"`
	ModRevision    int64  `json:"mod_revision,omitempty"`
}

// txRequest is the JSON payload for POST /tx.
type txRequest struct {
	Compares   []txCompare `json:"compares"`
	SuccessOps []batchOp   `json:"success_ops"`
	FailureOps []batchOp   `json:"failure_ops"`
}

// txResult is the JSON payload returned by POST /tx.
type txResult struct {
	Succeeded bool              `json:"succeeded"`
	Revision  int64             `json:"revision"`
	Responses []batchOpResponse `json:"responses"`
}

// httpTx sends a POST request to /tx with the given transaction request and
// returns the result. Retries up to 3 times on transient errors.
func httpTx(t *testing.T, ca *testCA, port int, req txRequest) txResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/tx", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("httpTx: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpTx: status %d, body: %s", resp.StatusCode, respBody)
		}
		var result txResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpTx: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpTx: %v (after %d attempts)", lastErr, maxRetries)
	return txResult{}
}

// ---------------------------------------------------------------------------
// MVCC helpers
// ---------------------------------------------------------------------------

// rangeResult is the JSON payload returned by GET /kv/range.
type rangeResult struct {
	Kvs      []kvResult `json:"kvs"`
	Count    int64      `json:"count"`
	Revision int64      `json:"revision,omitempty"`
}

// revisionResult is the JSON payload returned by GET /kv/revision.
type revisionResult struct {
	Revision int64 `json:"revision"`
}

// httpRange sends a GET request to /kv/range with the given start and end
// keys and returns the range result. Retries up to 3 times on transient errors.
func httpRange(t *testing.T, ca *testCA, port int, start, end string) rangeResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/range?start=%s", port, start)
	if end != "" {
		url += "&end=" + end
	}
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpRange: status %d, body: %s", resp.StatusCode, body)
		}
		var result rangeResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpRange: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpRange: %v (after %d attempts)", lastErr, maxRetries)
	return rangeResult{}
}

// httpCompact sends a POST request to /kv/compact with the given revision.
// Retries up to 3 times on transient errors.
func httpCompact(t *testing.T, ca *testCA, port int, revision int64) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/compact", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Revision int64 `json:"revision"`
	}{Revision: revision}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpCompact: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpCompact: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpCompact: %v (after %d attempts)", lastErr, maxRetries)
}

// httpCurrentRevision sends a GET request to /kv/revision and returns the
// current MVCC revision. Retries up to 3 times on transient errors.
func httpCurrentRevision(t *testing.T, ca *testCA, port int) int64 {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/revision", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpCurrentRevision: status %d, body: %s", resp.StatusCode, body)
		}
		var result revisionResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpCurrentRevision: decode: %v", decErr)
		}
		return result.Revision
	}
	t.Fatalf("httpCurrentRevision: %v (after %d attempts)", lastErr, maxRetries)
	return 0
}

// httpGetWithRevision sends a GET request to /kv/{key}?revision=true and
// returns the full MVCC key-value result. Retries up to 3 times on transient
// errors.
func httpGetWithRevision(t *testing.T, ca *testCA, port int, key string) kvResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s?revision=true", port, key)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpGetWithRevision %s: status %d, body: %s", key, resp.StatusCode, body)
		}
		var result kvResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpGetWithRevision: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpGetWithRevision: %v (after %d attempts)", lastErr, maxRetries)
	return kvResult{}
}

// ---------------------------------------------------------------------------
// Lease helpers
// ---------------------------------------------------------------------------

// leaseResult is the JSON representation of a lease.
type leaseResult struct {
	ID           int64    `json:"id"`
	TTL          int64    `json:"ttl"`
	GrantedTTL   int64    `json:"granted_ttl"`
	RemainingTTL int64    `json:"remaining_ttl"`
	Keys         []string `json:"keys"`
}

// leasesResult is the JSON payload returned by GET /lease/list.
type leasesResult struct {
	Leases []leaseResult `json:"leases"`
}

// httpLeaseGrant sends a POST request to /lease/grant with the given TTL
// and returns the lease result. Retries up to 3 times on transient errors.
func httpLeaseGrant(t *testing.T, ca *testCA, port int, ttl int64) leaseResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/lease/grant", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		TTL int64 `json:"ttl"`
	}{TTL: ttl}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpLeaseGrant: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpLeaseGrant: status %d, body: %s", resp.StatusCode, respBody)
		}
		var result leaseResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpLeaseGrant: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpLeaseGrant: %v (after %d attempts)", lastErr, maxRetries)
	return leaseResult{}
}

// httpLeaseRevoke sends a POST request to /lease/revoke with the given
// lease ID. Retries up to 3 times on transient errors.
func httpLeaseRevoke(t *testing.T, ca *testCA, port int, leaseID int64) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/lease/revoke", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		LeaseID int64 `json:"lease_id"`
	}{LeaseID: leaseID}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpLeaseRevoke: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpLeaseRevoke: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpLeaseRevoke: %v (after %d attempts)", lastErr, maxRetries)
}

// httpLeaseKeepAlive sends a POST request to /lease/keepalive with the given
// lease ID and returns the refreshed lease result. Retries up to 3 times on
// transient errors.
func httpLeaseKeepAlive(t *testing.T, ca *testCA, port int, leaseID int64) leaseResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/lease/keepalive", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		LeaseID int64 `json:"lease_id"`
	}{LeaseID: leaseID}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpLeaseKeepAlive: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpLeaseKeepAlive: status %d, body: %s", resp.StatusCode, respBody)
		}
		var result leaseResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpLeaseKeepAlive: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpLeaseKeepAlive: %v (after %d attempts)", lastErr, maxRetries)
	return leaseResult{}
}

// httpLeaseTTL sends a GET request to /lease/ttl?lease_id=N and returns the
// lease with its remaining TTL. Retries up to 3 times on transient errors.
func httpLeaseTTL(t *testing.T, ca *testCA, port int, leaseID int64) leaseResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/lease/ttl?lease_id=%d", port, leaseID)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpLeaseTTL: status %d, body: %s", resp.StatusCode, body)
		}
		var result leaseResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpLeaseTTL: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpLeaseTTL: %v (after %d attempts)", lastErr, maxRetries)
	return leaseResult{}
}

// httpLeaseList sends a GET request to /lease/list and returns all active
// leases. Retries up to 3 times on transient errors.
func httpLeaseList(t *testing.T, ca *testCA, port int) []leaseResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/lease/list", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpLeaseList: status %d, body: %s", resp.StatusCode, body)
		}
		var result leasesResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpLeaseList: decode: %v", decErr)
		}
		return result.Leases
	}
	t.Fatalf("httpLeaseList: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// ---------------------------------------------------------------------------
// Time Series helpers
// ---------------------------------------------------------------------------

// tsDataPoint is the JSON representation of a time-series data point.
type tsDataPoint struct {
	Metric    string            `json:"metric"`
	Value     float64           `json:"value"`
	Timestamp int64             `json:"timestamp"`
	Tags      map[string]string `json:"tags,omitempty"`
}

// tsQueryResult is the JSON payload returned by GET /ts/query.
type tsQueryResult struct {
	Points []tsDataPoint `json:"points"`
}

// tsMetricsResult is the JSON payload returned by GET /ts/metrics.
type tsMetricsResult struct {
	Metrics []string `json:"metrics"`
}

// tsAggResult is the JSON representation of an aggregation result bucket.
type tsAggResult struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
	Count     int64   `json:"count"`
}

// tsAggregateResult is the JSON payload returned by GET /ts/aggregate.
type tsAggregateResult struct {
	Results []tsAggResult `json:"results"`
}

// httpTSWritePoint sends a POST request to /ts/write to store a single
// time-series data point. The timestamp is set to the current time in
// nanoseconds. Retries up to 3 times on transient errors.
func httpTSWritePoint(t *testing.T, ca *testCA, port int, metric string, value float64, tags map[string]string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/ts/write", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Metric    string            `json:"metric"`
		Value     float64           `json:"value"`
		Timestamp int64             `json:"timestamp"`
		Tags      map[string]string `json:"tags,omitempty"`
	}{
		Metric:    metric,
		Value:     value,
		Timestamp: time.Now().UnixNano(),
		Tags:      tags,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpTSWritePoint: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpTSWritePoint: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpTSWritePoint: %v (after %d attempts)", lastErr, maxRetries)
}

// httpTSQuery sends a GET request to /ts/query with the given metric name
// and time range (nanoseconds) and returns the data points. Retries up to
// 3 times on transient errors.
func httpTSQuery(t *testing.T, ca *testCA, port int, metric string, startNs, endNs int64) []tsDataPoint {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/ts/query?metric=%s&start_ns=%d&end_ns=%d",
		port, metric, startNs, endNs)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpTSQuery: status %d, body: %s", resp.StatusCode, body)
		}
		var result tsQueryResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpTSQuery: decode: %v", decErr)
		}
		return result.Points
	}
	t.Fatalf("httpTSQuery: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// httpTSListMetrics sends a GET request to /ts/metrics and returns the list
// of available metric names. Retries up to 3 times on transient errors.
func httpTSListMetrics(t *testing.T, ca *testCA, port int) []string {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/ts/metrics", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpTSListMetrics: status %d, body: %s", resp.StatusCode, body)
		}
		var result tsMetricsResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpTSListMetrics: decode: %v", decErr)
		}
		return result.Metrics
	}
	t.Fatalf("httpTSListMetrics: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// httpTSDeleteSeries sends a DELETE request to /ts/series/{metric} to remove
// all data for the given metric. Retries up to 3 times on transient errors.
func httpTSDeleteSeries(t *testing.T, ca *testCA, port int, metric string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/ts/series/%s", port, metric)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		req, reqErr := http.NewRequest(http.MethodDelete, url, nil)
		if reqErr != nil {
			t.Fatalf("httpTSDeleteSeries: new request: %v", reqErr)
		}
		resp, doErr := client.Do(req)
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpTSDeleteSeries: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpTSDeleteSeries: %v (after %d attempts)", lastErr, maxRetries)
}

// httpTSAggregate sends a GET request to /ts/aggregate with the given
// parameters and returns the aggregation results. Retries up to 3 times
// on transient errors.
func httpTSAggregate(t *testing.T, ca *testCA, port int, metric string, startNs, endNs int64, fn string, intervalNs int64) []tsAggResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/ts/aggregate?metric=%s&start_ns=%d&end_ns=%d&function=%s&interval_ns=%d",
		port, metric, startNs, endNs, fn, intervalNs)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpTSAggregate: status %d, body: %s", resp.StatusCode, body)
		}
		var result tsAggregateResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpTSAggregate: decode: %v", decErr)
		}
		return result.Results
	}
	t.Fatalf("httpTSAggregate: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// ---------------------------------------------------------------------------
// Index helpers
// ---------------------------------------------------------------------------

// indexQueryResult is the JSON payload returned by GET /index/query.
type indexQueryResult struct {
	Keys []string `json:"keys"`
}

// httpIndexCreate sends a POST request to /index/create to create a new
// secondary index. Retries up to 3 times on transient errors.
func httpIndexCreate(t *testing.T, ca *testCA, port int, name, fieldName string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/index/create", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Name      string `json:"name"`
		FieldName string `json:"field_name"`
	}{Name: name, FieldName: fieldName}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpIndexCreate: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpIndexCreate: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpIndexCreate: %v (after %d attempts)", lastErr, maxRetries)
}

// httpIndexDrop sends a POST request to /index/drop to remove a secondary
// index by name. Retries up to 3 times on transient errors.
func httpIndexDrop(t *testing.T, ca *testCA, port int, name string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/index/drop", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Name string `json:"name"`
	}{Name: name}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpIndexDrop: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpIndexDrop: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpIndexDrop: %v (after %d attempts)", lastErr, maxRetries)
}

// httpIndexQuery sends a GET request to /index/query with the given index
// name and value, and returns the matching entity IDs. Retries up to 3 times
// on transient errors.
func httpIndexQuery(t *testing.T, ca *testCA, port int, indexName, value string) []string {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/index/query?index_name=%s&value=%s",
		port, indexName, value)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpIndexQuery: status %d, body: %s", resp.StatusCode, body)
		}
		var result indexQueryResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpIndexQuery: decode: %v", decErr)
		}
		return result.Keys
	}
	t.Fatalf("httpIndexQuery: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// ---------------------------------------------------------------------------
// Vector helpers
// ---------------------------------------------------------------------------

// vectorSearchResult is the JSON representation of a vector search result.
type vectorSearchResult struct {
	ID       uint64                 `json:"id"`
	Score    float32                `json:"score"`
	Vector   []float32              `json:"vector,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Distance float32                `json:"distance"`
	Rank     int                    `json:"rank"`
}

// vectorSearchResponse is the JSON payload returned by POST /vector/search/similar.
type vectorSearchResponse struct {
	Results       []vectorSearchResult `json:"results"`
	Timestamp     int64                `json:"timestamp"`
	QueryDuration int64                `json:"query_duration"`
}

// vectorDataResult is the JSON representation of a single stored vector.
type vectorDataResult struct {
	ID        uint64                 `json:"id"`
	Vector    []float32              `json:"vector"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Version   uint64                 `json:"version"`
	CreatedAt int64                  `json:"created_at"`
	UpdatedAt int64                  `json:"updated_at"`
}

// vectorListNSResponse is the JSON payload returned by GET /vector/namespaces.
type vectorListNSResponse struct {
	Namespaces []vectorNamespaceInfo `json:"namespaces"`
	TotalCount int                   `json:"total_count"`
	Timestamp  int64                 `json:"timestamp"`
}

// vectorNamespaceInfo is the JSON representation of namespace info in lists.
type vectorNamespaceInfo struct {
	Namespace      string            `json:"namespace"`
	Dimension      uint32            `json:"dimension"`
	VectorCount    uint64            `json:"vector_count"`
	DistanceMetric string            `json:"distance_metric"`
	IndexType      string            `json:"index_type"`
	IndexConfig    map[string]string `json:"index_config,omitempty"`
	CreatedAt      int64             `json:"created_at"`
	UpdatedAt      int64             `json:"updated_at"`
}

// httpVectorCreateNS sends a POST request to /vector/namespace to create a
// new vector namespace. Retries up to 3 times on transient errors.
func httpVectorCreateNS(t *testing.T, ca *testCA, port int, namespace string, dimension int, metric string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/vector/namespace", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Namespace      string `json:"namespace"`
		Dimension      uint32 `json:"dimension"`
		DistanceMetric string `json:"distance_metric"`
	}{
		Namespace:      namespace,
		Dimension:      uint32(dimension),
		DistanceMetric: metric,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpVectorCreateNS: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpVectorCreateNS: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpVectorCreateNS: %v (after %d attempts)", lastErr, maxRetries)
}

// httpVectorDeleteNS sends a POST request to /vector/namespace/delete to
// remove a vector namespace and all its vectors. Retries up to 3 times on
// transient errors.
func httpVectorDeleteNS(t *testing.T, ca *testCA, port int, namespace string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/vector/namespace/delete", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Namespace string `json:"namespace"`
	}{Namespace: namespace}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpVectorDeleteNS: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpVectorDeleteNS: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpVectorDeleteNS: %v (after %d attempts)", lastErr, maxRetries)
}

// httpVectorUpsert sends a POST request to /vector/upsert to insert or
// update a vector in the given namespace. Retries up to 3 times on transient
// errors.
func httpVectorUpsert(t *testing.T, ca *testCA, port int, namespace string, id uint64, vector []float32, metadata map[string]interface{}) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/vector/upsert", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Namespace string                 `json:"namespace"`
		ID        uint64                 `json:"id"`
		Vector    []float32              `json:"vector"`
		Metadata  map[string]interface{} `json:"metadata,omitempty"`
	}{
		Namespace: namespace,
		ID:        id,
		Vector:    vector,
		Metadata:  metadata,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpVectorUpsert: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpVectorUpsert: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpVectorUpsert: %v (after %d attempts)", lastErr, maxRetries)
}

// httpVectorSearch sends a POST request to /vector/search/similar to perform
// a k-nearest-neighbor search and returns the results. Retries up to 3 times
// on transient errors.
func httpVectorSearch(t *testing.T, ca *testCA, port int, namespace string, vector []float32, topK int) []vectorSearchResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/vector/search/similar", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Namespace   string    `json:"namespace"`
		QueryVector []float32 `json:"query_vector"`
		K           int       `json:"k"`
	}{
		Namespace:   namespace,
		QueryVector: vector,
		K:           topK,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpVectorSearch: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpVectorSearch: status %d, body: %s", resp.StatusCode, respBody)
		}
		var result vectorSearchResponse
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpVectorSearch: decode: %v", decErr)
		}
		return result.Results
	}
	t.Fatalf("httpVectorSearch: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// httpVectorGet sends a GET request to /vector/get?namespace=N&id=I and
// returns the stored vector data. Retries up to 3 times on transient errors.
func httpVectorGet(t *testing.T, ca *testCA, port int, namespace string, id uint64) vectorDataResult {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/vector/get?namespace=%s&id=%d", port, namespace, id)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpVectorGet: status %d, body: %s", resp.StatusCode, body)
		}
		var result vectorDataResult
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpVectorGet: decode: %v", decErr)
		}
		return result
	}
	t.Fatalf("httpVectorGet: %v (after %d attempts)", lastErr, maxRetries)
	return vectorDataResult{}
}

// httpVectorDeleteVec sends a POST request to /vector/batch/delete to remove
// vectors by their IDs from the given namespace. Retries up to 3 times on
// transient errors.
func httpVectorDeleteVec(t *testing.T, ca *testCA, port int, namespace string, ids []uint64) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/vector/batch/delete", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	payload := struct {
		Namespace string   `json:"namespace"`
		IDs       []uint64 `json:"ids"`
	}{
		Namespace: namespace,
		IDs:       ids,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("httpVectorDeleteVec: marshal: %v", err)
	}
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, doErr := client.Post(url, "application/json", bytes.NewReader(body))
		if doErr != nil {
			lastErr = doErr
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpVectorDeleteVec: status %d, body: %s", resp.StatusCode, respBody)
		}
		io.ReadAll(resp.Body)
		return
	}
	t.Fatalf("httpVectorDeleteVec: %v (after %d attempts)", lastErr, maxRetries)
}

// httpVectorListNS sends a GET request to /vector/namespaces and returns the
// list of namespace names. Retries up to 3 times on transient errors.
func httpVectorListNS(t *testing.T, ca *testCA, port int) []string {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/vector/namespaces", port)
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	const maxRetries = 3
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("httpVectorListNS: status %d, body: %s", resp.StatusCode, body)
		}
		var result vectorListNSResponse
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
			t.Fatalf("httpVectorListNS: decode: %v", decErr)
		}
		names := make([]string, len(result.Namespaces))
		for i, ns := range result.Namespaces {
			names[i] = ns.Namespace
		}
		return names
	}
	t.Fatalf("httpVectorListNS: %v (after %d attempts)", lastErr, maxRetries)
	return nil
}

// ---------------------------------------------------------------------------
// Phase 4c (IsolationRaft) helpers
// ---------------------------------------------------------------------------

// generateNodeCertWithSPIFFE creates a leaf certificate signed by this CA
// with a SPIFFE URI SAN of the form spiffe://<trustDomain>/node/<raftAddr>.
// This is required for Phase 4c-1c cluster key delivery: the sender's
// fetchNodeKey verifies the recipient certificate carries the SPIFFE SAN and
// rejects the transfer when it is absent (ErrRecipientUntrusted).
func (ca *testCA) generateNodeCertWithSPIFFE(t *testing.T, raftAddr, trustDomain string) (certPath, keyPath string) {
	t.Helper()
	kp := ca.issue(t, t.TempDir(),
		qrdbdharness.WithSubject("test-node", ""),
		qrdbdharness.WithServerAuth(),
		qrdbdharness.WithSPIFFEID(trustDomain, "node/"+raftAddr),
	)
	return kp.CertPath, kp.KeyPath
}

// generateClientCertForTenant creates a client certificate signed by this CA
// with CN=admin and OU=<tenantID>. The mTLS authenticator derives the caller
// identity from the certificate fields: OU becomes the tenant ID and CN
// becomes the username. This cert is used to invoke tenant-scoped REST
// endpoints (e.g. POST /v1/databases) as the given tenant.
func (ca *testCA) generateClientCertForTenant(t *testing.T, tenantID string) (certPath, keyPath string) {
	t.Helper()
	kp := ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(adminUser, tenantID))
	return kp.CertPath, kp.KeyPath
}

// mtlsHTTPClientFromFiles creates an mTLS HTTP client using cert/key PEM
// files rather than generating an in-memory admin cert. Use this when a test
// needs a non-system-tenant identity (e.g. OU=t1 for IsolationRaft flows).
func mtlsHTTPClientFromFiles(t *testing.T, ca *testCA, certPath, keyPath string, timeout time.Duration) *http.Client {
	t.Helper()

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("mtlsHTTPClientFromFiles: load key pair: %v", err)
	}
	return qrdbdharness.HTTPSClient(ca.Pool, &cert, timeout)
}

// writeIsolationRaftConfig writes a minimal qrdbd YAML configuration file
// that enables IsolationRaft tenant databases. It sets:
//   - seal.enabled: true with passphrase strategy (required for barrier-isolated
//     tenants; the sealed-window phase must be driven through init/unseal before
//     the full server starts — see initAndUnsealBarrier)
//   - storage.volume_pools with the given volumePoolDir as the sole path
//   - tenant.default_entitlements allowing raft isolation + dedicated raft
//   - system-managed barriers
//   - tenant.default_quotas with unlimited databases/raft-groups/replicas
//   - auth.peer_admin_urls mapping each peer's Raft address to its admin HTTPS
//     URL (required for Phase 4c-1c key delivery in single-host test clusters
//     where each node uses a distinct admin port)
//
// peerAdminURLs maps raftAddr (e.g. "127.0.0.1:24576") to adminURL (e.g.
// "https://127.0.0.1:24579"). Pass nil to omit the mapping (uses fallback).
//
// Returns the path to the written config file.
func writeIsolationRaftConfig(t *testing.T, volumePoolDir string, peerAdminURLs map[string]string) string {
	t.Helper()

	// Build the peer_admin_urls YAML block as a list of {raft_addr, admin_url}
	// structs. A list is required because Viper uses "." as its internal key
	// delimiter, which causes dotted IP addresses used as YAML map keys to be
	// parsed as nested path components and decoded incorrectly.
	peerAdminSection := ""
	if len(peerAdminURLs) > 0 {
		lines := "  peer_admin_urls:\n"
		for raftAddr, adminURL := range peerAdminURLs {
			lines += fmt.Sprintf("    - raft_addr: %q\n      admin_url: %q\n", raftAddr, adminURL)
		}
		peerAdminSection = "auth:\n" + lines
	}

	yaml := fmt.Sprintf(`seal:
  enabled: true
  strategy: passphrase
storage:
  volume_pools:
    - name: default
      paths:
        - %s
      strategy: round_robin
tenant:
  default_entitlements:
    allowed_isolation_modes:
      - prefix
      - raft
    allow_dedicated_raft: true
    allow_system_managed_barrier: true
    allow_dedicated_wal: true
  default_quotas:
    max_databases: -1
    max_raft_groups: -1
    max_replicas_per_group: -1
    max_total_replicas: -1
    max_dedicated_wal_groups: -1
%s`, volumePoolDir, peerAdminSection)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "qrdbd.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0600); err != nil {
		t.Fatalf("writeIsolationRaftConfig: write: %v", err)
	}
	return cfgPath
}

// waitForBarrierStatusReachable polls GET /v1/barrier/status on the given
// admin port until a 200 is received, indicating the sealed-window server is
// up and accepting requests. Uses mtlsHTTPClient because the server requires
// a client certificate for TLS even during the sealed-window phase.
func waitForBarrierStatusReachable(t *testing.T, ca *testCA, port int, timeout time.Duration) {
	t.Helper()

	rawURL := fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/status", port)
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(rawURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for barrier status at port %d", timeout, port)
}

// initAndUnsealBarrier initializes and unseals the system barrier on the
// given admin port. It calls POST /v1/barrier/initialize followed by
// POST /v1/barrier/unseal with the provided passphrase. The sealed-window
// server (which starts before the Raft host when seal.enabled=true) accepts
// these calls without RBAC because auth middleware is absent there — the
// barrier credential itself is the authorization.
func initAndUnsealBarrier(t *testing.T, ca *testCA, port int, passphrase string) {
	t.Helper()

	client := mtlsHTTPClient(t, ca, 10*time.Second)

	// Initialize the barrier.
	initBody, initStatus := iraftAPIPost(t, client,
		fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/initialize", port),
		map[string]interface{}{"secret": []byte(passphrase)},
	)
	if initStatus != http.StatusNoContent {
		t.Fatalf("barrier init on port %d: status %d, body: %s", port, initStatus, initBody)
	}

	// Unseal the barrier. A successful Initialize already transitions the
	// barrier to the unsealed state (pkg/seal: StorageBarrier.Initialize sets
	// stateUnsealed), so this explicit unseal is redundant on the first boot and
	// the daemon answers 409 ErrAlreadyUnsealed. Accept that as success: the
	// only goal here is to guarantee the barrier ends up unsealed. A genuine
	// unseal failure (wrong passphrase, sealed without init) still surfaces.
	//
	// Because Initialize already unseals the barrier, the daemon's boot
	// goroutine can start (and even finish) tearing down the pre-host
	// sealed-window listener before this explicit call lands, so a
	// "connection refused" here is the same benign race as the 409 case
	// above rather than a genuine failure -- retry briefly to absorb it,
	// and tolerate the connection continuing to be refused (the listener
	// really is gone because the barrier really is unsealed).
	body, err := json.Marshal(map[string]interface{}{"secret": []byte(passphrase)})
	if err != nil {
		t.Fatalf("initAndUnsealBarrier: marshal unseal body: %v", err)
	}
	unsealURL := fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/unseal", port)
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, postErr := client.Post(unsealURL, "application/json", bytes.NewReader(body))
		if postErr == nil {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusConflict {
				t.Fatalf("barrier unseal on port %d: status %d, body: %s", port, resp.StatusCode, respBody)
			}
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// iraftAPIPost sends a JSON POST to the given URL using the provided client and
// body. It returns the response body bytes and status code without fataling on
// non-2xx responses, allowing callers to inspect errors.
func iraftAPIPost(t *testing.T, client *http.Client, rawURL string, body interface{}) ([]byte, int) {
	t.Helper()

	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("iraftAPIPost marshal: %v", err)
	}

	resp, err := client.Post(rawURL, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("iraftAPIPost %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode
}

// iraftAPIGet performs a GET request using the provided client and returns the
// response body bytes and status code.
func iraftAPIGet(t *testing.T, client *http.Client, rawURL string) ([]byte, int) {
	t.Helper()

	resp, err := client.Get(rawURL)
	if err != nil {
		t.Fatalf("iraftAPIGet %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode
}

// ---------------------------------------------------------------------------
// Dedicated-raft database scoped KV helpers
// ---------------------------------------------------------------------------

// dbScopedPutTolerant sends a single PUT /kv/{key} with the
// X-QRDB-Database header set to dbName using the provided client. The header
// causes the REST service registry to route the request to the tenant's
// dedicated Raft shard (IsolationRaft path in restServiceRegistry.resolve).
//
// It returns (statusCode, error) without fataling on transient failures
// (network errors, 5xx, 503 forwarding). It calls t.Fatalf immediately on
// HTTP 401/403 since those indicate a configuration error (wrong role
// permissions), not a transient condition. Use this function inside polling
// loops to allow the dedicated group time to elect a leader.
func dbScopedPutTolerant(t *testing.T, client *http.Client, port int, dbName, key string, value []byte) (int, error) {
	t.Helper()
	rawURL := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	req, err := http.NewRequest(http.MethodPut, rawURL, bytes.NewReader(value))
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-QRDB-Database", dbName)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Fatalf("dbScopedPutTolerant %s (db=%s): auth error status=%d body=%s",
			key, dbName, resp.StatusCode, body)
	}
	return resp.StatusCode, nil
}

// dbScopedGetTolerant sends a single GET /kv/{key} with the
// X-QRDB-Database header set to dbName using the provided client.
//
// It returns (body, statusCode) without fataling on transient failures
// (network errors, 404 while the dedicated group is recovering). It calls
// t.Fatalf immediately on HTTP 401/403. Use this function inside polling
// loops to wait for keys to become visible on a restarted dedicated shard.
func dbScopedGetTolerant(t *testing.T, client *http.Client, port int, dbName, key string) ([]byte, int) {
	t.Helper()
	rawURL := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0
	}
	req.Header.Set("X-QRDB-Database", dbName)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Fatalf("dbScopedGetTolerant %s (db=%s): auth error status=%d body=%s",
			key, dbName, resp.StatusCode, body)
	}
	return body, resp.StatusCode
}

// waitForShardCount polls GET /admin/topology on the given port until
// ShardCount >= wantCount, or the deadline expires. It is used to verify that
// the reconciler (Phase 4c-1d) has started the dedicated raft shard — which it
// only does once the tenant barrier is unsealed — proving barrier key delivery
// (Phase 4c-1c) also succeeded.
func waitForShardCount(t *testing.T, client *http.Client, port int, wantCount int, timeout time.Duration) {
	t.Helper()

	rawURL := fmt.Sprintf("https://127.0.0.1:%d/admin/topology", port)
	deadline := time.Now().Add(timeout)
	var lastCount int
	var lastBody string
	for time.Now().Before(deadline) {
		body, status := iraftAPIGet(t, client, rawURL)
		if status == http.StatusOK {
			var topo struct {
				ShardCount int `json:"shard_count"`
			}
			if err := json.Unmarshal(body, &topo); err == nil {
				lastCount = topo.ShardCount
				if topo.ShardCount >= wantCount {
					return
				}
			}
		}
		lastBody = string(body)
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for shard count >= %d on port %d (last count: %d, last body: %s)",
		timeout, wantCount, port, lastCount, lastBody)
}
