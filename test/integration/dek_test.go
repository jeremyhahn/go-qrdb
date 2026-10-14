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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// writeDEKKeyFile provisions the built-in "software" key-wrap provider's
// key file for the tenant DEK keyring's default key reference -- the
// caller's tenant ID itself, "system" for every call these tests make
// (mTLS-authenticated, no tenant header) -- inside dir, so --dek-key-dir
// resolves it.
func writeDEKKeyFile(t *testing.T, dir, tenantID string) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	if err := os.WriteFile(filepath.Join(dir, tenantID), key, 0o600); err != nil {
		t.Fatalf("write DEK key file: %v", err)
	}
}

// The tenant DEK keyring end to end against real qrdbd processes: a keyring
// record written through one node is replicated through the system shard, so
// every node decrypts what any node encrypted, sees every rotation, and
// honours every retirement; and the versions survive a full restart.

// dekCall POSTs (or GETs, for status) a keyring request to a node and
// decodes the JSON reply into out. It retries transport errors and 5xx
// answers until the deadline, because a follower that has not yet applied
// the latest entry answers the moment the cluster settles; any 4xx is the
// node's final word and is returned as the status.
func dekCall(t *testing.T, ca *testCA, port int, method, path string, body, out any) int {
	t.Helper()
	client := mtlsHTTPClient(t, ca, 10*time.Second)
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			t.Fatalf("marshal: %v", err)
		}
	}
	url := fmt.Sprintf("https://127.0.0.1:%d%s", port, path)
	deadline := time.After(30 * time.Second)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var last string
	for {
		req, err := http.NewRequest(method, url, bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			data, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				t.Fatalf("read body: %v", readErr)
			}
			if resp.StatusCode < 500 {
				if resp.StatusCode == http.StatusOK && out != nil {
					if err := json.Unmarshal(data, out); err != nil {
						t.Fatalf("decode %s: %v (%s)", path, err, data)
					}
				}
				return resp.StatusCode
			}
			last = fmt.Sprintf("status %d: %s", resp.StatusCode, data)
		} else {
			last = err.Error()
		}
		select {
		case <-deadline:
			t.Fatalf("%s %s on port %d never answered: %s", method, path, port, last)
		case <-tick.C:
		}
	}
}

// mustDEK is dekCall that requires 200.
func mustDEK(t *testing.T, ca *testCA, port int, method, path string, body, out any) {
	t.Helper()
	if code := dekCall(t, ca, port, method, path, body, out); code != http.StatusOK {
		t.Fatalf("%s %s on port %d: status %d", method, path, port, code)
	}
}

// decryptsOn reports the plaintext a node returns for ciphertext.
func decryptsOn(t *testing.T, ca *testCA, port int, ciphertext, aad []byte) []byte {
	t.Helper()
	var dec transport.TenantDEKDecryptResponse
	mustDEK(t, ca, port, http.MethodPost, "/v1/dek/decrypt",
		transport.TenantDEKDecryptRequest{Ciphertext: ciphertext, AAD: aad}, &dec)
	return dec.Plaintext
}

func TestDEK_Cluster_EveryNodeDecryptsEveryVersion(t *testing.T) {
	dekKeyDir := t.TempDir()
	writeDEKKeyFile(t, dekKeyDir, "system")
	ports, _, ca := startThreeNodeCluster(t, "memory", "--dek-key-dir", dekKeyDir)
	aad := []byte("system/db/password")

	// Version 1 is created by the first encryption, on node 1.
	var v1 transport.TenantDEKEncryptResponse
	mustDEK(t, ca, ports[0], http.MethodPost, "/v1/dek/encrypt",
		transport.TenantDEKEncryptRequest{Plaintext: []byte("first"), AAD: aad}, &v1)
	if v1.Version != 1 {
		t.Fatalf("first encryption sealed under v%d, want v1", v1.Version)
	}

	// Rotate on node 2; node 3 encrypts under the new version.
	var st transport.TenantDEKStatusResponse
	mustDEK(t, ca, ports[1], http.MethodPost, "/v1/dek/rotate", transport.TenantDEKRequest{}, &st)
	if st.CurrentVersion != 2 {
		t.Fatalf("rotate on node 2: current v%d, want v2", st.CurrentVersion)
	}
	var v2 transport.TenantDEKEncryptResponse
	mustDEK(t, ca, ports[2], http.MethodPost, "/v1/dek/encrypt",
		transport.TenantDEKEncryptRequest{Plaintext: []byte("second"), AAD: aad}, &v2)
	if v2.Version != 2 {
		t.Fatalf("node 3 sealed under v%d after the rotation, want v2", v2.Version)
	}

	// Every node decrypts both versions.
	for i, port := range ports {
		if got := decryptsOn(t, ca, port, v1.Ciphertext, aad); !bytes.Equal(got, []byte("first")) {
			t.Errorf("node %d: v1 decrypts to %q", i+1, got)
		}
		if got := decryptsOn(t, ca, port, v2.Ciphertext, aad); !bytes.Equal(got, []byte("second")) {
			t.Errorf("node %d: v2 decrypts to %q", i+1, got)
		}
	}

	// Rewrap the v1 ciphertext on node 1; the result reads on node 3.
	var rw transport.TenantDEKRewrapResponse
	mustDEK(t, ca, ports[0], http.MethodPost, "/v1/dek/rewrap",
		transport.TenantDEKRewrapRequest{Ciphertext: v1.Ciphertext, AAD: aad}, &rw)
	if rw.FromVersion != 1 || rw.ToVersion != 2 {
		t.Fatalf("rewrap: v%d -> v%d, want v1 -> v2", rw.FromVersion, rw.ToVersion)
	}
	if got := decryptsOn(t, ca, ports[2], rw.Ciphertext, aad); !bytes.Equal(got, []byte("first")) {
		t.Errorf("node 3: rewrapped ciphertext decrypts to %q", got)
	}

	// A mismatched AAD fails on every node.
	for i, port := range ports {
		code := dekCall(t, ca, port, http.MethodPost, "/v1/dek/decrypt",
			transport.TenantDEKDecryptRequest{Ciphertext: v2.Ciphertext, AAD: []byte("other")}, nil)
		if code != http.StatusBadRequest {
			t.Errorf("node %d: mismatched AAD answered %d, want 400", i+1, code)
		}
	}

	// The current version cannot be retired; v1 can, on node 3, and from then
	// on no node decrypts it.
	if code := dekCall(t, ca, ports[0], http.MethodPost, "/v1/dek/retire",
		transport.TenantDEKRetireRequest{Version: 2}, nil); code != http.StatusConflict {
		t.Fatalf("retiring the current version answered %d, want 409", code)
	}
	mustDEK(t, ca, ports[2], http.MethodPost, "/v1/dek/retire", transport.TenantDEKRetireRequest{Version: 1}, &st)
	for i, port := range ports {
		code := dekCall(t, ca, port, http.MethodPost, "/v1/dek/decrypt",
			transport.TenantDEKDecryptRequest{Ciphertext: v1.Ciphertext, AAD: aad}, nil)
		if code != http.StatusConflict {
			t.Errorf("node %d: a retired version answered %d, want 409", i+1, code)
		}
	}

	// Naming another tenant is refused.
	if code := dekCall(t, ca, ports[1], http.MethodGet, "/v1/dek/status?tenant_id=someone-else", nil, nil); code != http.StatusForbidden {
		t.Errorf("cross-tenant status answered %d, want 403", code)
	}
}

// TestDEK_VersionsSurviveARestart stops a durable single node after two
// versions exist and checks both still decrypt, and the keyring still reports
// both, once it is back.
func TestDEK_VersionsSurviveARestart(t *testing.T) {
	bin := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 1)
	adminPorts := allocateTCPPorts(t, 1)
	grpcPorts := allocateTCPPorts(t, 1)
	dataDir := t.TempDir()
	dekKeyDir := t.TempDir()
	writeDEKKeyFile(t, dekKeyDir, "system")
	certPath, keyPath := ca.generateNodeCert(t)
	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", ports[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[0]),
		"--data-dir", dataDir,
		"--sm", "pebble",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", ports[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPorts[0]),
		"--grpc-port", fmt.Sprintf("%d", grpcPorts[0]),
		"--tls-ca-file", ca.CertPath,
		"--tls-cert-file", certPath,
		"--tls-key-file", keyPath,
		"--dek-key-dir", dekKeyDir,
	}
	// boot is bootstrapNodes on the first start and unsealNodes on the
	// restart: the barrier the first boot initialized survives the restart
	// sealed.
	start := func(prefix string, boot func(*testing.T, *testCA, ...int)) *exec.Cmd {
		cmd := startNode(t, bin, args, prefix)
		boot(t, ca, adminPorts[0])
		waitForAdmin(t, ca.Pool, adminPorts[0], 90*time.Second)
		waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
		return cmd
	}

	first := start("[dek-R1] ", bootstrapNodes)
	aad := []byte("restart")
	var v1, v2 transport.TenantDEKEncryptResponse
	mustDEK(t, ca, adminPorts[0], http.MethodPost, "/v1/dek/encrypt",
		transport.TenantDEKEncryptRequest{Plaintext: []byte("one"), AAD: aad}, &v1)
	mustDEK(t, ca, adminPorts[0], http.MethodPost, "/v1/dek/rotate", transport.TenantDEKRequest{}, nil)
	mustDEK(t, ca, adminPorts[0], http.MethodPost, "/v1/dek/encrypt",
		transport.TenantDEKEncryptRequest{Plaintext: []byte("two"), AAD: aad}, &v2)

	if err := first.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	_ = awaitNodeExit(t, first, nodeStopTimeout)

	start("[dek-R2] ", unsealNodes)
	if got := decryptsOn(t, ca, adminPorts[0], v1.Ciphertext, aad); !bytes.Equal(got, []byte("one")) {
		t.Errorf("v1 after restart decrypts to %q", got)
	}
	if got := decryptsOn(t, ca, adminPorts[0], v2.Ciphertext, aad); !bytes.Equal(got, []byte("two")) {
		t.Errorf("v2 after restart decrypts to %q", got)
	}
	var st transport.TenantDEKStatusResponse
	mustDEK(t, ca, adminPorts[0], http.MethodGet, "/v1/dek/status", nil, &st)
	if st.CurrentVersion != 2 || len(st.Versions) != 2 {
		t.Errorf("status after restart = %+v, want two versions with v2 current", st)
	}
}
