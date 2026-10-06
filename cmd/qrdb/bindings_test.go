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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// testLogger returns a logger configured for test output (debug level, console format).
func testLogger(t *testing.T) *logger.Logger {
	t.Helper()
	log, err := logger.New(logger.LogConfig{
		Level:     "debug",
		Format:    "console",
		Output:    "stdout",
		Component: "test",
	})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	return log
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

// newTestConnectionBinding constructs a ConnectionBinding backed by an
// embedded transport wired to the provided mock services.
func newTestConnectionBinding(
	t *testing.T,
	kvSvc service.KVServicer,
	adminSvc service.AdminServicer,
	opts ...embedded.Option,
) *ConnectionBinding {
	t.Helper()
	trans, err := embedded.New(kvSvc, adminSvc, opts...)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(trans)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	conn := NewConnectionBinding(testLogger(t))
	conn.SetClient(cl)
	return conn
}

// ---------------------------------------------------------------------------
// ConnectionBinding tests
// ---------------------------------------------------------------------------

func TestConnectionBinding_IsConnected_NoClient(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	if conn.IsConnected() {
		t.Fatal("expected IsConnected() == false for a fresh binding")
	}
}

func TestConnectionBinding_IsConnected_WithClient(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	if !conn.IsConnected() {
		t.Fatal("expected IsConnected() == true after SetClient")
	}
}

func TestConnectionBinding_Disconnect_NoClient(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	if err := conn.Disconnect(); err != nil {
		t.Fatalf("expected nil error from Disconnect on empty binding, got: %v", err)
	}
	if conn.IsConnected() {
		t.Fatal("expected IsConnected() == false after Disconnect")
	}
}

func TestConnectionBinding_Disconnect_WithClient(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	if err := conn.Disconnect(); err != nil {
		t.Fatalf("expected nil error from Disconnect, got: %v", err)
	}
	if conn.IsConnected() {
		t.Fatal("expected IsConnected() == false after Disconnect")
	}
}

func TestConnectionBinding_LoadClient_NoClient(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	_, err := conn.loadClient()
	if err == nil {
		t.Fatal("expected NotConnectedError from loadClient on empty binding")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestConnectionBinding_LoadClient_WithClient(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("expected nil error from loadClient, got: %v", err)
	}
	if cl == nil {
		t.Fatal("expected non-nil client from loadClient")
	}
}

func TestConnectionBinding_GetVersion(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	v := conn.GetVersion()
	if v == nil {
		t.Fatal("expected non-nil version map")
	}
	for _, key := range []string{"version", "gitCommit", "buildDate"} {
		if _, ok := v[key]; !ok {
			t.Fatalf("expected key %q in version map", key)
		}
	}
}

func TestConnectionBinding_GetVersion_Values(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	v := conn.GetVersion()
	if v["version"] != version {
		t.Fatalf("expected version=%q, got: %q", version, v["version"])
	}
	if v["gitCommit"] != gitCommit {
		t.Fatalf("expected gitCommit=%q, got: %q", gitCommit, v["gitCommit"])
	}
	if v["buildDate"] != buildDate {
		t.Fatalf("expected buildDate=%q, got: %q", buildDate, v["buildDate"])
	}
}

func TestConnectionBinding_SetClient(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	if conn.IsConnected() {
		t.Fatal("expected not connected before SetClient")
	}
	trans, err := embedded.New(newMockKVService(), newMockAdminService())
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(trans)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	conn.SetClient(cl)
	if !conn.IsConnected() {
		t.Fatal("expected connected after SetClient")
	}
}

func TestConnectionBinding_SetClient_Replaces(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	cl1, _ := conn.loadClient()

	trans2, err := embedded.New(newMockKVService(), newMockAdminService())
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl2, err := sdk.NewClient(trans2)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	conn.SetClient(cl2)

	cl, _ := conn.loadClient()
	if cl == cl1 {
		t.Fatal("expected SetClient to replace the old client")
	}
	if cl != cl2 {
		t.Fatal("expected loadClient to return the new client")
	}
}

type testCtxKey struct{}

func TestConnectionBinding_Startup(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	ctx := context.WithValue(context.Background(), testCtxKey{}, "test")
	conn.startup(ctx)
	if conn.ctx != ctx {
		t.Fatal("expected startup to store the provided context")
	}
}

func TestConnectionBinding_Shutdown_NoClient(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	conn.shutdown(context.Background())
	// No panic and client remains nil.
	if conn.IsConnected() {
		t.Fatal("expected not connected after shutdown with nil client")
	}
}

func TestConnectionBinding_Shutdown_WithClient(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	conn.shutdown(context.Background())
	// The client pointer is NOT cleared by shutdown (only by Disconnect),
	// but the underlying transport is closed.
}

// ---------------------------------------------------------------------------
// KVBinding tests
// ---------------------------------------------------------------------------

func TestKVBinding_Put_Success(t *testing.T) {
	kvSvc := newMockKVService()
	conn := newTestConnectionBinding(t, kvSvc, newMockAdminService())
	b := NewKVBinding(testLogger(t), conn)

	if err := b.Put("hello", "world"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if string(kvSvc.store["hello"]) != "world" {
		t.Fatalf("expected stored value 'world', got: %q", kvSvc.store["hello"])
	}
}

func TestKVBinding_Put_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewKVBinding(testLogger(t), conn)

	err := b.Put("key", "val")
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestKVBinding_Get_Success(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["greet"] = []byte("hi")
	conn := newTestConnectionBinding(t, kvSvc, newMockAdminService())
	b := NewKVBinding(testLogger(t), conn)

	val, err := b.Get("greet")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if val != "hi" {
		t.Fatalf("expected 'hi', got: %q", val)
	}
}

func TestKVBinding_Get_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewKVBinding(testLogger(t), conn)

	_, err := b.Get("key")
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestKVBinding_Get_KeyNotFound(t *testing.T) {
	kvSvc := newMockKVService()
	conn := newTestConnectionBinding(t, kvSvc, newMockAdminService())
	b := NewKVBinding(testLogger(t), conn)

	_, err := b.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}
	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *transport.KeyNotFoundError, got: %T: %v", err, err)
	}
}

func TestKVBinding_Delete_Success(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["del-me"] = []byte("val")
	conn := newTestConnectionBinding(t, kvSvc, newMockAdminService())
	b := NewKVBinding(testLogger(t), conn)

	if err := b.Delete("del-me"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if _, ok := kvSvc.store["del-me"]; ok {
		t.Fatal("expected key to be deleted from store")
	}
}

func TestKVBinding_Delete_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewKVBinding(testLogger(t), conn)

	err := b.Delete("key")
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestKVBinding_List_Success(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["app:1"] = []byte("a")
	kvSvc.store["app:2"] = []byte("b")
	kvSvc.store["other"] = []byte("c")
	conn := newTestConnectionBinding(t, kvSvc, newMockAdminService())
	b := NewKVBinding(testLogger(t), conn)

	keys, err := b.List("app:")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got: %d", len(keys))
	}
}

func TestKVBinding_List_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewKVBinding(testLogger(t), conn)

	_, err := b.List("")
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestKVBinding_List_EmptyPrefix(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["x"] = []byte("1")
	kvSvc.store["y"] = []byte("2")
	conn := newTestConnectionBinding(t, kvSvc, newMockAdminService())
	b := NewKVBinding(testLogger(t), conn)

	keys, err := b.List("")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got: %d", len(keys))
	}
}

func TestKVBinding_Exists_Found(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["exists-key"] = []byte("val")
	conn := newTestConnectionBinding(t, kvSvc, newMockAdminService())
	b := NewKVBinding(testLogger(t), conn)

	found, err := b.Exists("exists-key")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !found {
		t.Fatal("expected Exists() == true for existing key")
	}
}

func TestKVBinding_Exists_NotFound(t *testing.T) {
	kvSvc := newMockKVService()
	conn := newTestConnectionBinding(t, kvSvc, newMockAdminService())
	b := NewKVBinding(testLogger(t), conn)

	found, err := b.Exists("missing-key")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if found {
		t.Fatal("expected Exists() == false for missing key")
	}
}

func TestKVBinding_Exists_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewKVBinding(testLogger(t), conn)

	_, err := b.Exists("key")
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestKVBinding_Scan_Success(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["ns:a"] = []byte("val-a")
	kvSvc.store["ns:b"] = []byte("val-b")
	kvSvc.store["other"] = []byte("val-c")
	conn := newTestConnectionBinding(t, kvSvc, newMockAdminService())
	b := NewKVBinding(testLogger(t), conn)

	result, err := b.Scan("ns:")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 pairs, got: %d", len(result))
	}
	if result["ns:a"] != "val-a" {
		t.Fatalf("expected 'val-a', got: %q", result["ns:a"])
	}
	if result["ns:b"] != "val-b" {
		t.Fatalf("expected 'val-b', got: %q", result["ns:b"])
	}
}

func TestKVBinding_Scan_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewKVBinding(testLogger(t), conn)

	_, err := b.Scan("")
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestKVBinding_Scan_EmptyPrefix(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["k1"] = []byte("v1")
	conn := newTestConnectionBinding(t, kvSvc, newMockAdminService())
	b := NewKVBinding(testLogger(t), conn)

	result, err := b.Scan("")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 pair, got: %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// AdminBinding tests
// ---------------------------------------------------------------------------

func TestAdminBinding_Health_Success(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	b := NewAdminBinding(testLogger(t), conn)

	resp, err := b.Health()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil HealthResponse")
	}
	if resp.Status != "ok" {
		t.Fatalf("expected status 'ok', got: %q", resp.Status)
	}
}

func TestAdminBinding_Health_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewAdminBinding(testLogger(t), conn)

	_, err := b.Health()
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestAdminBinding_Status_Success(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	b := NewAdminBinding(testLogger(t), conn)

	resp, err := b.Status()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil StatusResponse")
	}
	if resp.NodeID != "node-1" {
		t.Fatalf("expected NodeID 'node-1', got: %q", resp.NodeID)
	}
}

func TestAdminBinding_Status_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewAdminBinding(testLogger(t), conn)

	_, err := b.Status()
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestAdminBinding_Peers_Success(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	b := NewAdminBinding(testLogger(t), conn)

	resp, err := b.Peers()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil PeersResponse")
	}
	if len(resp.Shards) != 1 {
		t.Fatalf("expected 1 shard, got: %d", len(resp.Shards))
	}
}

func TestAdminBinding_Peers_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewAdminBinding(testLogger(t), conn)

	_, err := b.Peers()
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestAdminBinding_AddMember_Success(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	b := NewAdminBinding(testLogger(t), conn)

	if err := b.AddMember(0, 3, "127.0.0.1:63102"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestAdminBinding_AddMember_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewAdminBinding(testLogger(t), conn)

	err := b.AddMember(0, 3, "127.0.0.1:63102")
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestAdminBinding_RemoveMember_Success(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	b := NewAdminBinding(testLogger(t), conn)

	if err := b.RemoveMember(0, 2); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestAdminBinding_RemoveMember_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewAdminBinding(testLogger(t), conn)

	err := b.RemoveMember(0, 2)
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestAdminBinding_RequestSnapshot_Success(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	b := NewAdminBinding(testLogger(t), conn)

	if err := b.RequestSnapshot(0); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestAdminBinding_RequestSnapshot_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewAdminBinding(testLogger(t), conn)

	err := b.RequestSnapshot(0)
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestAdminBinding_Drain_Success(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	b := NewAdminBinding(testLogger(t), conn)

	if err := b.Drain(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestAdminBinding_Drain_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewAdminBinding(testLogger(t), conn)

	err := b.Drain()
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestAdminBinding_Capabilities_Success(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	b := NewAdminBinding(testLogger(t), conn)

	resp, err := b.Capabilities()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil CapabilitiesResponse")
	}
	if !resp.KV {
		t.Fatal("expected KV capability to be true")
	}
}

func TestAdminBinding_Capabilities_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewAdminBinding(testLogger(t), conn)

	_, err := b.Capabilities()
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// MVCCBinding tests
// ---------------------------------------------------------------------------

func newTestConnWithMVCC(t *testing.T) (*ConnectionBinding, *mockMVCCService) {
	t.Helper()
	mvccSvc := newMockMVCCService()
	opts := []embedded.Option{
		embedded.WithMVCCService(mvccSvc),
	}
	conn := newTestConnectionBinding(t, mvccSvc, newMockAdminService(), opts...)
	return conn, mvccSvc
}

func TestMVCCBinding_PutWithLease_Success(t *testing.T) {
	conn, mvccSvc := newTestConnWithMVCC(t)
	b := NewMVCCBinding(testLogger(t), conn)

	resp, err := b.PutWithLease("lease-key", "lease-val", 42)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil MVCCKeyValue response")
	}
	if resp.LeaseID != 42 {
		t.Fatalf("expected LeaseID=42, got: %d", resp.LeaseID)
	}
	if string(mvccSvc.store["lease-key"]) != "lease-val" {
		t.Fatalf("expected stored value 'lease-val', got: %q", mvccSvc.store["lease-key"])
	}
}

func TestMVCCBinding_PutWithLease_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewMVCCBinding(testLogger(t), conn)

	_, err := b.PutWithLease("k", "v", 1)
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestMVCCBinding_GetRevision_Success(t *testing.T) {
	conn, mvccSvc := newTestConnWithMVCC(t)
	mvccSvc.store["rev-key"] = []byte("rev-val")
	b := NewMVCCBinding(testLogger(t), conn)

	resp, err := b.GetRevision("rev-key")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil MVCCKeyValue response")
	}
	if string(resp.Value) != "rev-val" {
		t.Fatalf("expected value 'rev-val', got: %q", string(resp.Value))
	}
	if resp.ModRevision != mvccSvc.revision {
		t.Fatalf("expected ModRevision=%d, got: %d", mvccSvc.revision, resp.ModRevision)
	}
}

func TestMVCCBinding_GetRevision_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewMVCCBinding(testLogger(t), conn)

	_, err := b.GetRevision("k")
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestMVCCBinding_GetRevision_KeyNotFound(t *testing.T) {
	conn, _ := newTestConnWithMVCC(t)
	b := NewMVCCBinding(testLogger(t), conn)

	_, err := b.GetRevision("missing")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestMVCCBinding_GetAtRevision_Success(t *testing.T) {
	conn, mvccSvc := newTestConnWithMVCC(t)
	mvccSvc.store["hist-key"] = []byte("hist-val")
	b := NewMVCCBinding(testLogger(t), conn)

	resp, err := b.GetAtRevision("hist-key", 10)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil MVCCKeyValue response")
	}
	if resp.ModRevision != 10 {
		t.Fatalf("expected ModRevision=10, got: %d", resp.ModRevision)
	}
}

func TestMVCCBinding_GetAtRevision_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewMVCCBinding(testLogger(t), conn)

	_, err := b.GetAtRevision("k", 1)
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestMVCCBinding_Range_Success(t *testing.T) {
	conn, mvccSvc := newTestConnWithMVCC(t)
	mvccSvc.store["a"] = []byte("1")
	mvccSvc.store["b"] = []byte("2")
	mvccSvc.store["c"] = []byte("3")
	b := NewMVCCBinding(testLogger(t), conn)

	resp, err := b.Range("a", "d", &transport.RangeOptions{Limit: 100})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil RangeResponse")
	}
	if resp.Count != int64(len(resp.Kvs)) {
		t.Fatalf("expected Count=%d to match len(Kvs)=%d", resp.Count, len(resp.Kvs))
	}
}

func TestMVCCBinding_Range_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewMVCCBinding(testLogger(t), conn)

	_, err := b.Range("a", "z", &transport.RangeOptions{Limit: 10})
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestMVCCBinding_Compact_Success(t *testing.T) {
	conn, _ := newTestConnWithMVCC(t)
	b := NewMVCCBinding(testLogger(t), conn)

	if err := b.Compact(10); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestMVCCBinding_Compact_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewMVCCBinding(testLogger(t), conn)

	err := b.Compact(10)
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestMVCCBinding_CurrentRevision_Success(t *testing.T) {
	conn, mvccSvc := newTestConnWithMVCC(t)
	b := NewMVCCBinding(testLogger(t), conn)

	rev, err := b.CurrentRevision()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if rev != mvccSvc.revision {
		t.Fatalf("expected revision=%d, got: %d", mvccSvc.revision, rev)
	}
}

func TestMVCCBinding_CurrentRevision_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewMVCCBinding(testLogger(t), conn)

	_, err := b.CurrentRevision()
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestMVCCBinding_RestoreToRevision_Success(t *testing.T) {
	conn, mvccSvc := newTestConnWithMVCC(t)
	mvccSvc.restoreFunc = func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		return &service.RestoreRevisionResult{
			Revision: req.Revision,
			DryRun:   req.DryRun,
			Skipped:  1,
			Changes: []service.RestoreKeyChange{
				{Key: "restore-key", Action: service.RestoreActionRevert, FromRevision: 5, ToRevision: 3},
			},
		}, nil
	}
	b := NewMVCCBinding(testLogger(t), conn)

	resp, err := b.RestoreToRevision(&transport.RestoreRevisionRequest{
		Revision: 3,
		Prefix:   []byte("restore-"),
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil RestoreRevisionResult")
	}
	if resp.Revision != 3 || !resp.DryRun {
		t.Fatalf("unexpected result: %+v", resp)
	}
	if len(resp.Changes) != 1 || string(resp.Changes[0].Key) != "restore-key" {
		t.Fatalf("unexpected changes: %+v", resp.Changes)
	}
}

func TestMVCCBinding_RestoreToRevision_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewMVCCBinding(testLogger(t), conn)

	_, err := b.RestoreToRevision(&transport.RestoreRevisionRequest{Revision: 3, Prefix: []byte("p")})
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestMVCCBinding_RestoreToRevision_ServiceError(t *testing.T) {
	conn, mvccSvc := newTestConnWithMVCC(t)
	mvccSvc.restoreFunc = func(context.Context, *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		return nil, &service.RestoreNilRequestError{}
	}
	b := NewMVCCBinding(testLogger(t), conn)

	_, err := b.RestoreToRevision(&transport.RestoreRevisionRequest{Revision: 3, Prefix: []byte("p")})
	if err == nil {
		t.Fatal("expected error to propagate from the service layer")
	}
}

// ---------------------------------------------------------------------------
// TransactionBinding tests
// ---------------------------------------------------------------------------

func TestTransactionBinding_Execute_Success(t *testing.T) {
	mvccSvc := newMockMVCCService()
	txSvc := &mockTransactionService{}
	opts := []embedded.Option{
		embedded.WithMVCCService(mvccSvc),
		embedded.WithTransactionService(txSvc),
	}
	conn := newTestConnectionBinding(t, mvccSvc, newMockAdminService(), opts...)
	b := NewTransactionBinding(testLogger(t), conn)

	req := &transport.TransactionRequest{
		Compares:   []transport.Compare{},
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte("tx-key"), Value: []byte("tx-val")}},
		FailureOps: []transport.Op{},
	}
	resp, err := b.Execute(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil TransactionResponse")
	}
	if !resp.Succeeded {
		t.Fatal("expected Succeeded == true")
	}
}

func TestTransactionBinding_Execute_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTransactionBinding(testLogger(t), conn)

	req := &transport.TransactionRequest{
		Compares:   []transport.Compare{},
		SuccessOps: []transport.Op{},
		FailureOps: []transport.Op{},
	}
	_, err := b.Execute(req)
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestTransactionBinding_Execute_WithCompares(t *testing.T) {
	mvccSvc := newMockMVCCService()
	txSvc := &mockTransactionService{}
	opts := []embedded.Option{
		embedded.WithMVCCService(mvccSvc),
		embedded.WithTransactionService(txSvc),
	}
	conn := newTestConnectionBinding(t, mvccSvc, newMockAdminService(), opts...)
	b := NewTransactionBinding(testLogger(t), conn)

	req := &transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte("cmp-key"), Target: transport.CompareVersion, Result: transport.CompareEqual, Version: 1},
		},
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte("s-key"), Value: []byte("s-val")}},
		FailureOps: []transport.Op{{Type: transport.OpDelete, Key: []byte("f-key")}},
	}
	resp, err := b.Execute(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("expected Succeeded == true")
	}
}

// ---------------------------------------------------------------------------
// BatchBinding tests
// ---------------------------------------------------------------------------

func TestBatchBinding_Execute_Success(t *testing.T) {
	mvccSvc := newMockMVCCService()
	batchSvc := &mockBatchService{}
	opts := []embedded.Option{
		embedded.WithMVCCService(mvccSvc),
		embedded.WithBatchService(batchSvc),
	}
	conn := newTestConnectionBinding(t, mvccSvc, newMockAdminService(), opts...)
	b := NewBatchBinding(testLogger(t), conn)

	ops := []WailsOp{
		{Type: transport.OpPut, Key: "b-key1", Value: "b-val1"},
		{Type: transport.OpPut, Key: "b-key2", Value: "b-val2"},
	}
	resp, err := b.Execute(ops)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil BatchResponse")
	}
	if len(resp.Responses) != 2 {
		t.Fatalf("expected 2 responses, got: %d", len(resp.Responses))
	}
}

func TestBatchBinding_Execute_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewBatchBinding(testLogger(t), conn)

	ops := []WailsOp{
		{Type: transport.OpPut, Key: "k", Value: "v"},
	}
	_, err := b.Execute(ops)
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestBatchBinding_Execute_EmptyOps(t *testing.T) {
	mvccSvc := newMockMVCCService()
	batchSvc := &mockBatchService{}
	opts := []embedded.Option{
		embedded.WithMVCCService(mvccSvc),
		embedded.WithBatchService(batchSvc),
	}
	conn := newTestConnectionBinding(t, mvccSvc, newMockAdminService(), opts...)
	b := NewBatchBinding(testLogger(t), conn)

	resp, err := b.Execute([]WailsOp{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil BatchResponse")
	}
	if len(resp.Responses) != 0 {
		t.Fatalf("expected 0 responses, got: %d", len(resp.Responses))
	}
}

// ---------------------------------------------------------------------------
// LeaseBinding tests
// ---------------------------------------------------------------------------

func newTestConnWithLease(t *testing.T) *ConnectionBinding {
	t.Helper()
	leaseSvc := &mockLeaseService{}
	opts := []embedded.Option{
		embedded.WithLeaseService(leaseSvc),
	}
	return newTestConnectionBinding(t, newMockKVService(), newMockAdminService(), opts...)
}

func TestLeaseBinding_Grant_Success(t *testing.T) {
	conn := newTestConnWithLease(t)
	b := NewLeaseBinding(testLogger(t), conn)

	resp, err := b.Grant(60)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil LeaseInfo")
	}
	if resp.TTL != 60 {
		t.Fatalf("expected TTL=60, got: %d", resp.TTL)
	}
}

func TestLeaseBinding_Grant_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewLeaseBinding(testLogger(t), conn)

	_, err := b.Grant(60)
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestLeaseBinding_Revoke_Success(t *testing.T) {
	conn := newTestConnWithLease(t)
	b := NewLeaseBinding(testLogger(t), conn)

	if err := b.Revoke(1); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestLeaseBinding_Revoke_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewLeaseBinding(testLogger(t), conn)

	err := b.Revoke(1)
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestLeaseBinding_KeepAlive_Success(t *testing.T) {
	conn := newTestConnWithLease(t)
	b := NewLeaseBinding(testLogger(t), conn)

	resp, err := b.KeepAlive(1)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil LeaseInfo")
	}
	if resp.ID != 1 {
		t.Fatalf("expected ID=1, got: %d", resp.ID)
	}
}

func TestLeaseBinding_KeepAlive_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewLeaseBinding(testLogger(t), conn)

	_, err := b.KeepAlive(1)
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestLeaseBinding_TimeToLive_Success(t *testing.T) {
	conn := newTestConnWithLease(t)
	b := NewLeaseBinding(testLogger(t), conn)

	resp, err := b.TimeToLive(1)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil LeaseInfo")
	}
	if resp.RemainingTTL != 30 {
		t.Fatalf("expected RemainingTTL=30, got: %d", resp.RemainingTTL)
	}
}

func TestLeaseBinding_TimeToLive_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewLeaseBinding(testLogger(t), conn)

	_, err := b.TimeToLive(1)
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestLeaseBinding_List_Success(t *testing.T) {
	conn := newTestConnWithLease(t)
	b := NewLeaseBinding(testLogger(t), conn)

	leases, err := b.List()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(leases) != 2 {
		t.Fatalf("expected 2 leases, got: %d", len(leases))
	}
}

func TestLeaseBinding_List_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewLeaseBinding(testLogger(t), conn)

	_, err := b.List()
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// WatchBinding tests
// ---------------------------------------------------------------------------

// noopEmitter is a no-op EventEmitter for tests that avoids calling into
// the Wails runtime (which requires a lifecycle-bound context).
func noopEmitter(_ context.Context, _ string, _ ...interface{}) {}

func TestWatchBinding_Watch_Success(t *testing.T) {
	watchSvc := newMockWatchService()
	opts := []embedded.Option{
		embedded.WithWatchService(watchSvc),
	}
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService(), opts...)
	b := NewWatchBindingWithEmitter(testLogger(t), conn, noopEmitter)

	watchID, err := b.Watch("watched-key", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if watchID == 0 {
		t.Fatal("expected non-zero watch ID")
	}
}

func TestWatchBinding_Watch_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewWatchBindingWithEmitter(testLogger(t), conn, noopEmitter)

	_, err := b.Watch("key", transport.WatchOptions{})
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

func TestWatchBinding_WatchPrefix_Success(t *testing.T) {
	watchSvc := newMockWatchService()
	opts := []embedded.Option{
		embedded.WithWatchService(watchSvc),
	}
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService(), opts...)
	b := NewWatchBindingWithEmitter(testLogger(t), conn, noopEmitter)

	watchID, err := b.WatchPrefix("watched", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if watchID == 0 {
		t.Fatal("expected non-zero watch ID")
	}
}

func TestWatchBinding_WatchPrefix_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewWatchBindingWithEmitter(testLogger(t), conn, noopEmitter)

	_, err := b.WatchPrefix("prefix", transport.WatchOptions{})
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestNotConnectedError_Message(t *testing.T) {
	err := &NotConnectedError{}
	expected := "qrdb: not connected to server"
	if err.Error() != expected {
		t.Fatalf("expected %q, got: %q", expected, err.Error())
	}
}

func TestNotConnectedError_Interface(t *testing.T) {
	var err error = &NotConnectedError{}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatal("expected errors.As to match *NotConnectedError")
	}
}

func TestGUIUnavailableError_Message(t *testing.T) {
	err := &GUIUnavailableError{}
	expected := "qrdb: GUI unavailable (built without desktop tag)"
	if err.Error() != expected {
		t.Fatalf("expected %q, got: %q", expected, err.Error())
	}
}

func TestGUIUnavailableError_Interface(t *testing.T) {
	var err error = &GUIUnavailableError{}
	var gue *GUIUnavailableError
	if !errors.As(err, &gue) {
		t.Fatal("expected errors.As to match *GUIUnavailableError")
	}
}

func TestFeatureDisabledError_Message(t *testing.T) {
	err := &FeatureDisabledError{Feature: "time-series"}
	expected := "qrdb: feature disabled: time-series"
	if err.Error() != expected {
		t.Fatalf("expected %q, got: %q", expected, err.Error())
	}
}

func TestFeatureDisabledError_Feature(t *testing.T) {
	err := &FeatureDisabledError{Feature: "vector"}
	if err.Feature != "vector" {
		t.Fatalf("expected Feature='vector', got: %q", err.Feature)
	}
}

func TestEmbeddedServerError_Message(t *testing.T) {
	inner := errors.New("bind failed")
	err := &EmbeddedServerError{Op: "startup", Err: inner}
	expected := "qrdb: embedded server startup: bind failed"
	if err.Error() != expected {
		t.Fatalf("expected %q, got: %q", expected, err.Error())
	}
}

func TestEmbeddedServerError_Unwrap(t *testing.T) {
	inner := errors.New("bind failed")
	err := &EmbeddedServerError{Op: "startup", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("expected Unwrap to return the inner error")
	}
}

func TestEmbeddedServerError_Op(t *testing.T) {
	err := &EmbeddedServerError{Op: "shutdown", Err: errors.New("timeout")}
	if err.Op != "shutdown" {
		t.Fatalf("expected Op='shutdown', got: %q", err.Op)
	}
}

func TestAlreadyRunningError_Message(t *testing.T) {
	err := &AlreadyRunningError{}
	expected := "qrdb: embedded server is already running"
	if err.Error() != expected {
		t.Fatalf("expected %q, got: %q", expected, err.Error())
	}
}

func TestAlreadyRunningError_Interface(t *testing.T) {
	var err error = &AlreadyRunningError{}
	var are *AlreadyRunningError
	if !errors.As(err, &are) {
		t.Fatal("expected errors.As to match *AlreadyRunningError")
	}
}

func TestNotRunningError_Message(t *testing.T) {
	err := &NotRunningError{}
	expected := "qrdb: embedded server is not running"
	if err.Error() != expected {
		t.Fatalf("expected %q, got: %q", expected, err.Error())
	}
}

func TestNotRunningError_Interface(t *testing.T) {
	var err error = &NotRunningError{}
	var nre *NotRunningError
	if !errors.As(err, &nre) {
		t.Fatal("expected errors.As to match *NotRunningError")
	}
}

// ---------------------------------------------------------------------------
// UserBinding not-connected tests
// ---------------------------------------------------------------------------

func TestUserBinding_Create_NotConnected(t *testing.T) {
	b := NewUserBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.Create(nil)
	assertNotConnected(t, err)
}

func TestUserBinding_Get_NotConnected(t *testing.T) {
	b := NewUserBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.Get("id")
	assertNotConnected(t, err)
}

func TestUserBinding_Update_NotConnected(t *testing.T) {
	b := NewUserBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.Update(nil)
	assertNotConnected(t, err)
}

func TestUserBinding_Delete_NotConnected(t *testing.T) {
	b := NewUserBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	err := b.Delete("id")
	assertNotConnected(t, err)
}

func TestUserBinding_List_NotConnected(t *testing.T) {
	b := NewUserBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.List(nil)
	assertNotConnected(t, err)
}

func TestUserBinding_Enable_NotConnected(t *testing.T) {
	b := NewUserBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	err := b.Enable("id")
	assertNotConnected(t, err)
}

func TestUserBinding_Disable_NotConnected(t *testing.T) {
	b := NewUserBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	err := b.Disable("id")
	assertNotConnected(t, err)
}

// ---------------------------------------------------------------------------
// RoleBinding not-connected tests
// ---------------------------------------------------------------------------

func TestRoleBinding_Create_NotConnected(t *testing.T) {
	b := NewRoleBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.Create(nil)
	assertNotConnected(t, err)
}

func TestRoleBinding_Get_NotConnected(t *testing.T) {
	b := NewRoleBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.Get("id")
	assertNotConnected(t, err)
}

func TestRoleBinding_Update_NotConnected(t *testing.T) {
	b := NewRoleBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.Update(nil)
	assertNotConnected(t, err)
}

func TestRoleBinding_Delete_NotConnected(t *testing.T) {
	b := NewRoleBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	err := b.Delete("id")
	assertNotConnected(t, err)
}

func TestRoleBinding_List_NotConnected(t *testing.T) {
	b := NewRoleBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.List(nil)
	assertNotConnected(t, err)
}

func TestRoleBinding_Assign_NotConnected(t *testing.T) {
	b := NewRoleBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	err := b.Assign("uid", "rid")
	assertNotConnected(t, err)
}

func TestRoleBinding_Unassign_NotConnected(t *testing.T) {
	b := NewRoleBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	err := b.Unassign("uid", "rid")
	assertNotConnected(t, err)
}

func TestRoleBinding_ListPermissions_NotConnected(t *testing.T) {
	b := NewRoleBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.ListPermissions()
	assertNotConnected(t, err)
}

// ---------------------------------------------------------------------------
// CertBinding not-connected tests
// ---------------------------------------------------------------------------

func TestCertBinding_Issue_NotConnected(t *testing.T) {
	b := NewCertBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.Issue(&IssueCertInput{Username: "alice", CSRPEM: "already-have-a-csr"})
	assertNotConnected(t, err)
}

func TestCertBinding_Enroll_NotConnected(t *testing.T) {
	b := NewCertBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.Enroll(&transport.EnrollCertRequest{Username: "alice"})
	assertNotConnected(t, err)
}

// TestCertBinding_Export_NilInput reflects Export's actual contract: it
// builds its PKCS#12 archive entirely locally (go-qrdb security review --
// no private key may reach a node), so it needs no connection at all, and a
// nil input is refused on its own terms rather than as "not connected".
func TestCertBinding_Export_NilInput(t *testing.T) {
	b := NewCertBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.Export(nil)
	var nilErr *ExportCertNilInputError
	if !errors.As(err, &nilErr) {
		t.Fatalf("Export(nil) = %v, want *ExportCertNilInputError", err)
	}
}

func TestCertBinding_Revoke_NotConnected(t *testing.T) {
	b := NewCertBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.Revoke("serial")
	assertNotConnected(t, err)
}

func TestCertBinding_List_NotConnected(t *testing.T) {
	b := NewCertBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.List("tenant")
	assertNotConnected(t, err)
}

// assertNotConnected verifies err is a *NotConnectedError.
func assertNotConnected(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got %T: %v", err, err)
	}
}
