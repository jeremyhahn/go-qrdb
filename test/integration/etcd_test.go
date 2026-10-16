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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// ---------------------------------------------------------------------------
// Build binary with etcd support
// ---------------------------------------------------------------------------

// buildBinaryWithEtcd compiles the qrdbd daemon binary with the qrdb_etcd
// build tag included. The harness caches builds per tag set, so this binary
// and the one buildBinary produces are compiled once each and never conflict.
func buildBinaryWithEtcd(t *testing.T) string {
	t.Helper()
	binary, err := qrdbdharness.BuildBinary(qrdbdharness.DaemonPackage,
		qrdbdharness.WithBuildTags(qrdbdharness.DefaultBuildTags+" qrdb_etcd"))
	if err != nil {
		t.Fatalf("buildBinaryWithEtcd: %v", err)
	}
	return binary
}

// ---------------------------------------------------------------------------
// Start node with etcd protocol
// ---------------------------------------------------------------------------

// startSingleNodeWithEtcd starts a single-node QRDB instance, creates a
// database with the etcd wire protocol enabled, and returns the admin port,
// the etcd gRPC port, and the test CA used for TLS.
func startSingleNodeWithEtcd(t *testing.T) (adminPort, etcdPort int, ca *testCA) {
	t.Helper()

	bin := buildBinaryWithEtcd(t)
	ca = generateTestCA(t)
	raftPorts := allocatePorts(t, 1)
	adminPorts := allocateTCPPorts(t, 1)
	grpcPorts := allocateTCPPorts(t, 1)
	etcdPorts := allocateTCPPorts(t, 1)
	dataDir := t.TempDir()

	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[0]),
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPorts[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPorts[0]),
		"--grpc-port", fmt.Sprintf("%d", grpcPorts[0]),
	}
	args = append(args, ca.tlsFlags(t)...)

	startNode(t, bin, args, "[etcd-node] ")
	bootstrapNodes(t, ca, adminPorts[0])
	waitForHealthy(t, dataDir, 30*time.Second)
	waitForAdmin(t, ca.Pool, adminPorts[0], 90*time.Second)
	waitForLeaderViaAdmin(t, ca, []int{adminPorts[0]}, 60*time.Second)
	warmupWrite(t, ca, adminPorts[0], 15*time.Second)

	// Create a database with etcd protocol enabled via REST API.
	createDatabaseWithEtcd(t, ca, adminPorts[0], "etcd-test", etcdPorts[0])

	// Wait for the etcd gRPC server to become ready.
	waitForEtcdReady(t, ca, etcdPorts[0], 30*time.Second)

	return adminPorts[0], etcdPorts[0], ca
}

// createDatabaseWithEtcd creates a database via the REST API and then
// enables etcd protocol on it via the PATCH endpoint.
func createDatabaseWithEtcd(t *testing.T, ca *testCA, adminPort int, dbName string, etcdPort int) {
	t.Helper()

	client := mtlsHTTPClient(t, ca, 10*time.Second)

	// Step 1: Create the database.
	createBody := map[string]interface{}{
		"name":    dbName,
		"backend": "pebble",
	}
	createJSON, err := json.Marshal(createBody)
	if err != nil {
		t.Fatalf("createDatabaseWithEtcd: marshal create: %v", err)
	}

	createURL := fmt.Sprintf("https://127.0.0.1:%d/v1/databases", adminPort)
	resp, err := client.Post(createURL, "application/json", bytes.NewReader(createJSON))
	if err != nil {
		t.Fatalf("createDatabaseWithEtcd: POST: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("createDatabaseWithEtcd: POST status %d, body: %s", resp.StatusCode, body)
	}

	// Step 2: Enable etcd protocol via PATCH.
	etcdBody := map[string]interface{}{
		"enabled":    true,
		"listenAddr": fmt.Sprintf("127.0.0.1:%d", etcdPort),
	}
	etcdJSON, err := json.Marshal(etcdBody)
	if err != nil {
		t.Fatalf("createDatabaseWithEtcd: marshal etcd: %v", err)
	}

	etcdURL := fmt.Sprintf("https://127.0.0.1:%d/v1/databases/%s/etcd", adminPort, dbName)
	req, err := http.NewRequest(http.MethodPatch, etcdURL, bytes.NewReader(etcdJSON))
	if err != nil {
		t.Fatalf("createDatabaseWithEtcd: new PATCH request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("createDatabaseWithEtcd: PATCH: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("createDatabaseWithEtcd: PATCH status %d, body: %s", resp.StatusCode, body)
	}
}

// ---------------------------------------------------------------------------
// etcd client helpers
// ---------------------------------------------------------------------------

// waitForEtcdReady polls the etcd gRPC server until it accepts a connection
// or the timeout expires.
func waitForEtcdReady(t *testing.T, ca *testCA, port int, timeout time.Duration) {
	t.Helper()

	tlsCfg := etcdClientTLS(t, ca)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cli, err := clientv3.New(clientv3.Config{
			Endpoints:   []string{fmt.Sprintf("https://127.0.0.1:%d", port)},
			DialTimeout: 2 * time.Second,
			TLS:         tlsCfg,
			DialOptions: []grpc.DialOption{
				grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
			},
		})
		if err != nil {
			cancel()
			time.Sleep(200 * time.Millisecond)
			continue
		}

		_, err = cli.Status(ctx, fmt.Sprintf("https://127.0.0.1:%d", port))
		cancel()
		cli.Close()
		if err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}

	t.Fatalf("waitForEtcdReady: timed out after %v waiting for etcd on port %d", timeout, port)
}

// etcdClientTLS returns the TLS configuration an etcd client presents: the
// admin client certificate. The etcd listener authenticates every RPC with
// the node's authenticator chain and refuses a client that presents none.
func etcdClientTLS(t *testing.T, ca *testCA) *tls.Config {
	t.Helper()
	_ = mtlsHTTPClient(t, ca, time.Second) // mints ca.adminCert once
	return &tls.Config{
		RootCAs:      ca.Pool,
		Certificates: []tls.Certificate{ca.adminCert},
		MinVersion:   tls.VersionTLS13,
	}
}

// newEtcdClient creates an etcd v3 client configured with TLS for the
// given port and test CA. The client is registered for cleanup via t.Cleanup.
func newEtcdClient(t *testing.T, ca *testCA, port int) *clientv3.Client {
	t.Helper()

	tlsCfg := etcdClientTLS(t, ca)

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{fmt.Sprintf("https://127.0.0.1:%d", port)},
		DialTimeout: 5 * time.Second,
		TLS:         tlsCfg,
		DialOptions: []grpc.DialOption{
			grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		},
	})
	if err != nil {
		t.Fatalf("newEtcdClient: %v", err)
	}
	t.Cleanup(func() { cli.Close() })

	return cli
}

// ---------------------------------------------------------------------------
// etcd integration tests
// ---------------------------------------------------------------------------

func TestEtcd_PutAndGet(t *testing.T) {
	_, etcdPort, ca := startSingleNodeWithEtcd(t)
	cli := newEtcdClient(t, ca, etcdPort)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Put a key-value pair.
	_, err := cli.Put(ctx, "/test/greeting", "hello world")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Get it back and verify.
	resp, err := cli.Get(ctx, "/test/greeting")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(resp.Kvs) != 1 {
		t.Fatalf("Get: expected 1 kv, got %d", len(resp.Kvs))
	}
	if string(resp.Kvs[0].Key) != "/test/greeting" {
		t.Errorf("key: got %q, want %q", string(resp.Kvs[0].Key), "/test/greeting")
	}
	if string(resp.Kvs[0].Value) != "hello world" {
		t.Errorf("value: got %q, want %q", string(resp.Kvs[0].Value), "hello world")
	}
}

func TestEtcd_DeleteRange(t *testing.T) {
	_, etcdPort, ca := startSingleNodeWithEtcd(t)
	cli := newEtcdClient(t, ca, etcdPort)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Put 3 keys.
	keys := []string{"/del/alpha", "/del/bravo", "/del/charlie"}
	for _, k := range keys {
		if _, err := cli.Put(ctx, k, "value-"+k); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	// Delete one key.
	delResp, err := cli.Delete(ctx, "/del/bravo")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if delResp.Deleted != 1 {
		t.Errorf("Delete count: got %d, want 1", delResp.Deleted)
	}

	// Verify deleted key is gone.
	getResp, err := cli.Get(ctx, "/del/bravo")
	if err != nil {
		t.Fatalf("Get deleted key: %v", err)
	}
	if len(getResp.Kvs) != 0 {
		t.Errorf("deleted key should be gone, got %d kvs", len(getResp.Kvs))
	}

	// Verify remaining keys exist.
	for _, k := range []string{"/del/alpha", "/del/charlie"} {
		getResp, err := cli.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get %s: %v", k, err)
		}
		if len(getResp.Kvs) != 1 {
			t.Errorf("Get %s: expected 1 kv, got %d", k, len(getResp.Kvs))
		}
	}
}

func TestEtcd_PrefixGet(t *testing.T) {
	_, etcdPort, ca := startSingleNodeWithEtcd(t)
	cli := newEtcdClient(t, ca, etcdPort)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Put keys under different prefixes.
	kvs := map[string]string{
		"/app/config/a": "cfg-a",
		"/app/config/b": "cfg-b",
		"/app/data/c":   "data-c",
	}
	for k, v := range kvs {
		if _, err := cli.Put(ctx, k, v); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	// Get with prefix /app/config/.
	resp, err := cli.Get(ctx, "/app/config/", clientv3.WithPrefix())
	if err != nil {
		t.Fatalf("Get with prefix: %v", err)
	}
	if len(resp.Kvs) != 2 {
		t.Fatalf("prefix Get: expected 2 kvs, got %d", len(resp.Kvs))
	}

	// Verify only config keys are returned.
	gotKeys := make(map[string]bool, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		gotKeys[string(kv.Key)] = true
	}
	if !gotKeys["/app/config/a"] {
		t.Error("prefix Get: missing /app/config/a")
	}
	if !gotKeys["/app/config/b"] {
		t.Error("prefix Get: missing /app/config/b")
	}
	if gotKeys["/app/data/c"] {
		t.Error("prefix Get: unexpected /app/data/c in config prefix")
	}
}

func TestEtcd_Txn(t *testing.T) {
	_, etcdPort, ca := startSingleNodeWithEtcd(t)
	cli := newEtcdClient(t, ca, etcdPort)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Put an initial value.
	_, err := cli.Put(ctx, "/txn/key", "initial")
	if err != nil {
		t.Fatalf("Put initial: %v", err)
	}

	// Execute Txn: If key value == "initial", then put new value.
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Value("/txn/key"), "=", "initial")).
		Then(clientv3.OpPut("/txn/key", "updated")).
		Else(clientv3.OpGet("/txn/key")).
		Commit()
	if err != nil {
		t.Fatalf("Txn: %v", err)
	}
	if !txnResp.Succeeded {
		t.Error("Txn: expected success, got failure")
	}

	// Verify the new value.
	getResp, err := cli.Get(ctx, "/txn/key")
	if err != nil {
		t.Fatalf("Get after Txn: %v", err)
	}
	if len(getResp.Kvs) != 1 {
		t.Fatalf("Get after Txn: expected 1 kv, got %d", len(getResp.Kvs))
	}
	if string(getResp.Kvs[0].Value) != "updated" {
		t.Errorf("value after Txn: got %q, want %q", string(getResp.Kvs[0].Value), "updated")
	}
}

func TestEtcd_Compact(t *testing.T) {
	_, etcdPort, ca := startSingleNodeWithEtcd(t)
	cli := newEtcdClient(t, ca, etcdPort)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Generate several revisions by putting and deleting keys.
	for i := range 5 {
		key := fmt.Sprintf("/compact/key-%d", i)
		if _, err := cli.Put(ctx, key, fmt.Sprintf("value-%d", i)); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}
	for i := range 3 {
		key := fmt.Sprintf("/compact/key-%d", i)
		if _, err := cli.Delete(ctx, key); err != nil {
			t.Fatalf("Delete %s: %v", key, err)
		}
	}

	// Get the current revision.
	statusResp, err := cli.Status(ctx, fmt.Sprintf("https://127.0.0.1:%d", etcdPort))
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	// Compact to the current revision.
	_, err = cli.Compact(ctx, statusResp.Header.Revision)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
}

func TestEtcd_LeaseGrantAndRevoke(t *testing.T) {
	_, etcdPort, ca := startSingleNodeWithEtcd(t)
	cli := newEtcdClient(t, ca, etcdPort)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Grant a lease with 60s TTL.
	leaseResp, err := cli.Grant(ctx, 60)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	leaseID := leaseResp.ID
	if leaseID == 0 {
		t.Fatal("Grant: lease ID is 0")
	}

	// Put a key with the lease.
	_, err = cli.Put(ctx, "/lease/key", "leased-value", clientv3.WithLease(leaseID))
	if err != nil {
		t.Fatalf("Put with lease: %v", err)
	}

	// Verify TTL via TimeToLive.
	ttlResp, err := cli.TimeToLive(ctx, leaseID)
	if err != nil {
		t.Fatalf("TimeToLive: %v", err)
	}
	if ttlResp.TTL <= 0 {
		t.Errorf("TTL: got %d, want > 0", ttlResp.TTL)
	}

	// Revoke the lease.
	_, err = cli.Revoke(ctx, leaseID)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// Verify key is gone after lease revocation.
	getResp, err := cli.Get(ctx, "/lease/key")
	if err != nil {
		t.Fatalf("Get after Revoke: %v", err)
	}
	if len(getResp.Kvs) != 0 {
		t.Errorf("key should be gone after lease revoke, got %d kvs", len(getResp.Kvs))
	}
}

// TestEtcd_LeaseKeepAliveAndExpiry drives renewal and expiry through a stock
// etcd client: KeepAliveOnce keeps a short lease, and the key bound to it,
// alive well past its granted TTL, reporting the full TTL each time; once
// keep-alives stop the sweeper revokes the lease and deletes the key, and a
// keep-alive on the gone lease is answered with TTL 0, which the client
// reports as ErrLeaseNotFound.
func TestEtcd_LeaseKeepAliveAndExpiry(t *testing.T) {
	_, etcdPort, ca := startSingleNodeWithEtcd(t)
	cli := newEtcdClient(t, ca, etcdPort)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const ttl = 3
	leaseResp, err := cli.Grant(ctx, ttl)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if _, err = cli.Put(ctx, "/lease/renewed", "v", clientv3.WithLease(leaseResp.ID)); err != nil {
		t.Fatalf("Put with lease: %v", err)
	}

	renewEvery := time.NewTicker(time.Second)
	defer renewEvery.Stop()
	renewUntil := time.After(2 * ttl * time.Second)
renewing:
	for {
		ka, kaErr := cli.KeepAliveOnce(ctx, leaseResp.ID)
		if kaErr != nil {
			t.Fatalf("KeepAliveOnce: %v", kaErr)
		}
		if ka.ID != leaseResp.ID || ka.TTL != ttl {
			t.Fatalf("KeepAliveOnce = {ID: %d, TTL: %d}, want {ID: %d, TTL: %d}", ka.ID, ka.TTL, leaseResp.ID, ttl)
		}
		select {
		case <-renewEvery.C:
		case <-renewUntil:
			break renewing
		}
	}
	getResp, err := cli.Get(ctx, "/lease/renewed")
	if err != nil || len(getResp.Kvs) != 1 {
		t.Fatalf("Get after renewing past the granted TTL = %v, err=%v; want the key", getResp, err)
	}

	awaitClusterCondition(t, 4*ttl*time.Second, "expiry of the unrenewed lease's key", func() error {
		got, getErr := cli.Get(ctx, "/lease/renewed")
		if getErr != nil {
			return getErr
		}
		if len(got.Kvs) != 0 {
			return &leaseKeyPresentError{Key: "/lease/renewed"}
		}
		return nil
	})

	if _, err = cli.KeepAliveOnce(ctx, leaseResp.ID); !errors.Is(err, rpctypes.ErrLeaseNotFound) {
		t.Fatalf("KeepAliveOnce on the expired lease: err = %v, want rpctypes.ErrLeaseNotFound", err)
	}
}

func TestEtcd_WatchPut(t *testing.T) {
	_, etcdPort, ca := startSingleNodeWithEtcd(t)
	cli := newEtcdClient(t, ca, etcdPort)

	watchCtx, watchCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer watchCancel()

	// Create a watch on a key.
	watchCh := cli.Watch(watchCtx, "/watch/key")

	// Put a value to trigger the watch event.
	putCtx, putCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer putCancel()

	_, err := cli.Put(putCtx, "/watch/key", "watched-value")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Verify the watch channel receives a PUT event with correct value.
	select {
	case resp := <-watchCh:
		if resp.Err() != nil {
			t.Fatalf("Watch error: %v", resp.Err())
		}
		if len(resp.Events) == 0 {
			t.Fatal("Watch: expected at least 1 event, got 0")
		}
		ev := resp.Events[0]
		if ev.Type != clientv3.EventTypePut {
			t.Errorf("event type: got %v, want PUT", ev.Type)
		}
		if string(ev.Kv.Key) != "/watch/key" {
			t.Errorf("event key: got %q, want %q", string(ev.Kv.Key), "/watch/key")
		}
		if string(ev.Kv.Value) != "watched-value" {
			t.Errorf("event value: got %q, want %q", string(ev.Kv.Value), "watched-value")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Watch: timed out waiting for event")
	}
}

func TestEtcd_MemberList(t *testing.T) {
	_, etcdPort, ca := startSingleNodeWithEtcd(t)
	cli := newEtcdClient(t, ca, etcdPort)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Call MemberList and verify at least one member is returned.
	resp, err := cli.MemberList(ctx)
	if err != nil {
		t.Fatalf("MemberList: %v", err)
	}
	if len(resp.Members) == 0 {
		t.Error("MemberList: expected at least 1 member, got 0")
	}
}

func TestEtcd_Status(t *testing.T) {
	_, etcdPort, ca := startSingleNodeWithEtcd(t)
	cli := newEtcdClient(t, ca, etcdPort)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Call Status and verify a non-error response.
	endpoint := fmt.Sprintf("https://127.0.0.1:%d", etcdPort)
	resp, err := cli.Status(ctx, endpoint)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.Header == nil {
		t.Fatal("Status: response header is nil")
	}
	t.Logf("Status: revision=%d, raftTerm=%d", resp.Header.Revision, resp.RaftTerm)
}

func TestEtcd_AuthStatus(t *testing.T) {
	_, etcdPort, ca := startSingleNodeWithEtcd(t)
	cli := newEtcdClient(t, ca, etcdPort)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The etcd Auth service is not served: callers authenticate with their
	// client certificate through the node's authenticator chain, and an RPC
	// the listener does not serve is refused rather than answered by a stub
	// claiming authentication is off.
	if _, err := cli.AuthStatus(ctx); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("AuthStatus: err = %v, want PermissionDenied", err)
	}
}

func TestEtcd_TwoDatabases_Isolated(t *testing.T) {
	bin := buildBinaryWithEtcd(t)
	ca := generateTestCA(t)
	raftPorts := allocatePorts(t, 1)
	adminPorts := allocateTCPPorts(t, 1)
	grpcPorts := allocateTCPPorts(t, 1)
	etcdPorts := allocateTCPPorts(t, 2)
	dataDir := t.TempDir()

	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[0]),
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPorts[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPorts[0]),
		"--grpc-port", fmt.Sprintf("%d", grpcPorts[0]),
	}
	args = append(args, ca.tlsFlags(t)...)

	startNode(t, bin, args, "[etcd-iso] ")
	bootstrapNodes(t, ca, adminPorts[0])
	waitForHealthy(t, dataDir, 30*time.Second)
	waitForAdmin(t, ca.Pool, adminPorts[0], 90*time.Second)
	waitForLeaderViaAdmin(t, ca, []int{adminPorts[0]}, 60*time.Second)
	warmupWrite(t, ca, adminPorts[0], 15*time.Second)

	// Create two databases, each with etcd enabled on different ports.
	createDatabaseWithEtcd(t, ca, adminPorts[0], "etcd-db1", etcdPorts[0])
	createDatabaseWithEtcd(t, ca, adminPorts[0], "etcd-db2", etcdPorts[1])

	waitForEtcdReady(t, ca, etcdPorts[0], 30*time.Second)
	waitForEtcdReady(t, ca, etcdPorts[1], 30*time.Second)

	cli1 := newEtcdClient(t, ca, etcdPorts[0])
	cli2 := newEtcdClient(t, ca, etcdPorts[1])

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Put a key via database 1's etcd endpoint.
	_, err := cli1.Put(ctx, "/multi/key1", "db1-value")
	if err != nil {
		t.Fatalf("Put to db1: %v", err)
	}

	// Put a different key via database 2's etcd endpoint.
	_, err = cli2.Put(ctx, "/multi/key2", "db2-value")
	if err != nil {
		t.Fatalf("Put to db2: %v", err)
	}

	// Verify database 1's key is readable from endpoint 1.
	resp1, err := cli1.Get(ctx, "/multi/key1")
	if err != nil {
		t.Fatalf("Get from db1: %v", err)
	}
	if len(resp1.Kvs) != 1 {
		t.Fatalf("db1 Get: expected 1 kv, got %d", len(resp1.Kvs))
	}
	if string(resp1.Kvs[0].Value) != "db1-value" {
		t.Errorf("db1 value: got %q, want %q", string(resp1.Kvs[0].Value), "db1-value")
	}

	// Verify database 2's key is readable from endpoint 2.
	resp2, err := cli2.Get(ctx, "/multi/key2")
	if err != nil {
		t.Fatalf("Get from db2: %v", err)
	}
	if len(resp2.Kvs) != 1 {
		t.Fatalf("db2 Get: expected 1 kv, got %d", len(resp2.Kvs))
	}
	if string(resp2.Kvs[0].Value) != "db2-value" {
		t.Errorf("db2 value: got %q, want %q", string(resp2.Kvs[0].Value), "db2-value")
	}

	// Each endpoint serves exactly its own database's keyspace. The two
	// databases share one Raft shard, but each listener resolves its
	// database's "<tenant>/<database>/" namespace, so a key written through
	// one endpoint is not visible through the other. This test used to
	// assert the opposite: both listeners served the raw shard.
	resp1Cross, err := cli1.Get(ctx, "/multi/key2")
	if err != nil {
		t.Fatalf("Get db2 key from db1 endpoint: %v", err)
	}
	if len(resp1Cross.Kvs) != 0 {
		t.Fatalf("db1's endpoint read db2's key: %q", resp1Cross.Kvs[0].Value)
	}

	resp2Cross, err := cli2.Get(ctx, "/multi/key1")
	if err != nil {
		t.Fatalf("Get db1 key from db2 endpoint: %v", err)
	}
	if len(resp2Cross.Kvs) != 0 {
		t.Fatalf("db2's endpoint read db1's key: %q", resp2Cross.Kvs[0].Value)
	}

	// A prefix read over the whole keyspace is confined as well.
	all, err := cli1.Get(ctx, "", clientv3.WithFromKey())
	if err != nil {
		t.Fatalf("Get all from db1 endpoint: %v", err)
	}
	for _, kv := range all.Kvs {
		if string(kv.Key) != "/multi/key1" {
			t.Errorf("db1's endpoint returned a key outside its database: %q", kv.Key)
		}
	}
}
