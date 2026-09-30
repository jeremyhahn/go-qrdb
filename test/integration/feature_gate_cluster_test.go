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
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// gatedDatabase is a database every node provisions from its configuration,
// so all three nodes resolve it and every write to it carries the drop-fence
// guard once the gate enables guarded writes.
const gatedDatabase = "gated"

// writeGatedDatabaseConfig writes a qrdbd configuration that provisions
// gatedDatabase beside the default database.
func writeGatedDatabaseConfig(t *testing.T) string {
	t.Helper()
	cfg := "databases:\n" +
		"  - name: system\n    engine: kv\n    backend: pebble\n" +
		"  - name: " + gatedDatabase + "\n    engine: kv\n    backend: pebble\n"
	path := filepath.Join(t.TempDir(), "qrdbd.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// kvRequest issues one KV request to port, scoped to db when db is set.
func kvRequest(t *testing.T, client *http.Client, method string, port int, db, key string, body []byte, query string) (int, []byte) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s%s", port, key, query)
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if db != "" {
		req.Header.Set("X-QRDB-Database", db)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// On a three-node cluster -- on both the default pebble state machine and
// the MVCC one -- every node advertises its features, the cluster activates
// guarded writes, and writes to a named database are then proposed guarded:
// with its drop-fence marker set, a write through any node is refused by the
// state machine, where an unguarded one would have landed.
func TestCluster_FeatureGateActivatesGuardedWrites(t *testing.T) {
	for _, sm := range []string{"pebble", "mvcc"} {
		t.Run(sm, func(t *testing.T) {
			adminPorts, _, ca, logs := startThreeNodeClusterLogged(t, sm, "--config", writeGatedDatabaseConfig(t))
			client := mtlsHTTPClient(t, ca, 10*time.Second)

			deadline := time.Now().Add(60 * time.Second)
			activated := func() bool {
				for _, l := range logs {
					if l.Contains("feature gate: feature activated cluster-wide") {
						return true
					}
				}
				return false
			}
			for i, l := range logs {
				for !l.Contains("registered cluster member") {
					if time.Now().After(deadline) {
						t.Fatalf("node %d never advertised its features", i+1)
					}
					time.Sleep(200 * time.Millisecond)
				}
			}
			for !activated() {
				if time.Now().After(deadline) {
					t.Fatal("the cluster never activated guarded writes")
				}
				time.Sleep(200 * time.Millisecond)
			}
			for i, l := range logs {
				if l.Contains("cannot run in this cluster") || l.Contains("failed to register self") {
					t.Fatalf("node %d refused or failed its advertisement", i+1)
				}
			}

			// A write to the named database lands on every replica.
			for i, port := range adminPorts {
				key := fmt.Sprintf("before-fence-%d", i)
				waitStatus(t, deadline, func() int {
					code, _ := kvRequest(t, client, http.MethodPut, port, gatedDatabase, key, []byte("v"), "")
					return code
				}, http.StatusNoContent, "write to %s through node %d", gatedDatabase, i+1)
			}

			// Drop the database through node 1. The catalog is node-local, so
			// nodes 2 and 3 still resolve it and admit a write to it; only
			// the guard the write carries -- evaluated by the state machine
			// against the tombstone the drop left -- refuses it. An unguarded
			// write through them would land in the dropped keyspace.
			waitStatus(t, deadline, func() int {
				req, err := http.NewRequest(http.MethodDelete,
					fmt.Sprintf("https://127.0.0.1:%d/v1/databases/%s", adminPorts[0], gatedDatabase), nil)
				if err != nil {
					t.Fatalf("new request: %v", err)
				}
				resp, err := client.Do(req)
				if err != nil {
					return 0
				}
				defer resp.Body.Close()
				return resp.StatusCode
			}, http.StatusNoContent, "drop %s through node 1", gatedDatabase)
			for i, port := range adminPorts[1:] {
				key := fmt.Sprintf("after-drop-%d", i)
				waitStatus(t, deadline, func() int {
					code, _ := kvRequest(t, client, http.MethodPut, port, gatedDatabase, key, []byte("v"), "")
					return code
				}, http.StatusConflict, "guarded write to the dropped %s through node %d", gatedDatabase, i+2)
				for j, readPort := range adminPorts[1:] {
					code, body := kvRequest(t, client, http.MethodGet, readPort, gatedDatabase, key, nil, "?stale=true")
					if code == http.StatusOK {
						t.Fatalf("node %d applied the refused write %q (%s)", j+2, key, strings.TrimSpace(string(body)))
					}
				}
			}
		})
	}
}

// waitStatus polls do until it returns want or the deadline passes.
func waitStatus(t *testing.T, deadline time.Time, do func() int, want int, format string, args ...any) {
	t.Helper()
	last := 0
	for time.Now().Before(deadline) {
		if last = do(); last == want {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf(format+": last status %d, want %d", append(args, last, want)...)
}

// A node stopped gracefully withdraws its feature advertisement at once --
// its lease is revoked while its host is still up -- rather than leaving the
// record to count until the lease expires (cluster.DefaultLeaseTTL).
func TestCluster_GracefulShutdownWithdrawsTheAdvertisement(t *testing.T) {
	_, _, _, logs := startThreeNodeClusterLogged(t, "pebble")
	deadline := time.Now().Add(60 * time.Second)
	for i, l := range logs {
		for !l.Contains("registered cluster member") {
			if time.Now().After(deadline) {
				t.Fatalf("node %d never advertised its features", i+1)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	stopped := time.Now()
	if err := logs[2].cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM node 3: %v", err)
	}
	const withdrawn = `feature gate: node withdrew its advertisement" node_id=3`
	for !logs[0].Contains(withdrawn) && !logs[1].Contains(withdrawn) {
		if time.Since(stopped) > 5*time.Second {
			t.Fatal("node 3's advertisement was not withdrawn within 5s of a graceful stop; " +
				"it was left to expire with its lease")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := logs[2].cmd.Wait(); err != nil {
		t.Fatalf("node 3 did not stop cleanly: %v", err)
	}
}
