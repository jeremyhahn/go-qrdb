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
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Mock services
// ---------------------------------------------------------------------------

// mockKVService implements service.KVServicer for testing.
type mockKVService struct {
	store map[string][]byte
}

// The compile-time interface check lives HERE, next to the type, and so does
// every method. Both used to sit in auth_bindings_coverage_test.go, which is
// `!integration`, while the type itself is unconstrained: under
// BUILD_TAGS+integration the double still compiled and had silently stopped
// satisfying service.KVServicer, failing at auth_cmd_test.go's call into
// embedded.New rather than anywhere near the cause (#203). A type's method set
// must not straddle a build constraint.
var _ service.KVServicer = (*mockKVService)(nil)

func newMockKVService() *mockKVService {
	return &mockKVService{store: make(map[string][]byte)}
}

func (m *mockKVService) Put(_ context.Context, key string, value []byte) error {
	m.store[key] = value
	return nil
}

func (m *mockKVService) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	if _, exists := m.store[key]; exists {
		return false, nil
	}
	m.store[key] = value
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *mockKVService) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	current, ok := m.store[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	delete(m.store, key)
	return true, nil
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*mockKVService) CompareAndSwap(_ context.Context, _ string, _, _ []byte) (bool, error) {
	return false, nil
}

func (m *mockKVService) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *mockKVService) Delete(_ context.Context, key string) error {
	delete(m.store, key)
	return nil
}

func (m *mockKVService) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *mockKVService) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
}

func (m *mockKVService) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, nil
}

func (m *mockKVService) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, nil
}

func (m *mockKVService) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, nil
}

func (m *mockKVService) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, nil
}

func (m *mockKVService) GetStale(ctx context.Context, key string) ([]byte, error) {
	return m.Get(ctx, key)
}

func (m *mockKVService) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}

func (m *mockKVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
}

func (m *mockKVService) PutWithMode(_ context.Context, key string, value []byte, _ writemode.Mode) error {
	m.store[key] = value
	return nil
}

func (m *mockKVService) DeleteWithMode(_ context.Context, key string, _ writemode.Mode) error {
	delete(m.store, key)
	return nil
}

func (m *mockKVService) GetWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) ([]byte, error) {
	return m.Get(ctx, key)
}

func (m *mockKVService) ListWithConsistency(ctx context.Context, prefix string, _ consistency.ReadOptions) ([]string, error) {
	return m.List(ctx, prefix)
}

func (m *mockKVService) ScanWithConsistency(ctx context.Context, prefix string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
}

func (m *mockKVService) ExistsWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) (bool, error) {
	v, err := m.Get(ctx, key)
	if err != nil {
		return false, nil
	}
	return v != nil, nil
}

// errorKVService implements service.KVServicer that returns errors.
type errorKVService struct{}

func (e *errorKVService) Put(_ context.Context, _ string, _ []byte) error {
	return &service.ProposeError{Err: errors.New("raft unavailable")}
}

func (e *errorKVService) PutIfAbsent(_ context.Context, _ string, _ []byte) (bool, error) {
	return false, &service.ProposeError{Err: errors.New("raft unavailable")}
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (e *errorKVService) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}

func (e *errorKVService) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, &service.ReadError{Err: errors.New("read failed")}
}

func (e *errorKVService) Delete(_ context.Context, _ string) error {
	return &service.ProposeError{Err: errors.New("raft unavailable")}
}

func (e *errorKVService) List(_ context.Context, _ string) ([]string, error) {
	return nil, &service.ReadError{Err: errors.New("read failed")}
}

func (e *errorKVService) Scan(_ context.Context, _ string) (map[string][]byte, error) {
	return nil, &service.ReadError{Err: errors.New("scan failed")}
}

func (e *errorKVService) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, nil
}

func (e *errorKVService) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, nil
}

func (e *errorKVService) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, nil
}

func (e *errorKVService) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, nil
}

func (e *errorKVService) GetStale(ctx context.Context, key string) ([]byte, error) {
	return e.Get(ctx, key)
}

func (e *errorKVService) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return e.List(ctx, prefix)
}

func (e *errorKVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return e.Scan(ctx, prefix)
}

func (e *errorKVService) PutWithMode(_ context.Context, _ string, _ []byte, _ writemode.Mode) error {
	return &service.ProposeError{Err: errors.New("raft unavailable")}
}

func (e *errorKVService) DeleteWithMode(_ context.Context, _ string, _ writemode.Mode) error {
	return &service.ProposeError{Err: errors.New("raft unavailable")}
}

func (e *errorKVService) GetWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]byte, error) {
	return nil, &service.ReadError{Err: errors.New("read failed")}
}

func (e *errorKVService) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	return nil, &service.ReadError{Err: errors.New("read failed")}
}

func (e *errorKVService) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return nil, &service.ReadError{Err: errors.New("scan failed")}
}

func (e *errorKVService) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	return false, &service.ReadError{Err: errors.New("read failed")}
}

// mockAdminService implements service.AdminServicer for testing.
type mockAdminService struct {
	drainErr    error
	snapshotErr error
}

func newMockAdminService() *mockAdminService {
	return &mockAdminService{}
}

func (m *mockAdminService) Health(_ context.Context) (*service.HealthStatus, error) {
	return &service.HealthStatus{Status: "ok", Leader: true}, nil
}

func (m *mockAdminService) Status(_ context.Context) (*service.StatusInfo, error) {
	return &service.StatusInfo{
		NodeID:      "node-1",
		RaftAddress: "127.0.0.1:63100",
		IsLeader:    true,
		LeaderID:    1,
		ShardCount:  2,
		IsDraining:  false,
	}, nil
}

func (m *mockAdminService) Peers(_ context.Context) (*service.PeersInfo, error) {
	return &service.PeersInfo{
		Shards: []service.ShardPeers{
			{
				ShardID:  0,
				LeaderID: 1,
				Peers: []service.PeerInfo{
					{ReplicaID: 1, Address: "127.0.0.1:63100", IsLeader: true},
					{ReplicaID: 2, Address: "127.0.0.1:63101", IsLeader: false},
				},
			},
		},
	}, nil
}

func (m *mockAdminService) AddMember(_ context.Context, _, _ uint64, _ string) error {
	return nil
}

func (m *mockAdminService) RemoveMember(_ context.Context, _, _ uint64) error {
	return nil
}

func (m *mockAdminService) RequestSnapshot(_ context.Context, _ uint64) error {
	return m.snapshotErr
}

func (m *mockAdminService) Drain(_ context.Context) error {
	return m.drainErr
}

func (m *mockAdminService) Capabilities(_ context.Context) (*capabilities.Capabilities, error) {
	return &capabilities.Capabilities{
		KV:          true,
		MVCC:        true,
		Transaction: true,
		Batch:       true,
		Watch:       true,
		Lease:       true,
		Index:       true,
	}, nil
}

func (m *mockAdminService) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	return nil
}

func (m *mockAdminService) StopShard(_ context.Context, _ uint64) error {
	return nil
}

func (m *mockAdminService) GetShardState(_ context.Context, _ uint64) (*service.ShardStateResponse, error) {
	return &service.ShardStateResponse{}, nil
}

func (m *mockAdminService) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	return &service.TopologyResponse{}, nil
}

// errorAdminService implements service.AdminServicer that returns errors.
type errorAdminService struct{}

func (e *errorAdminService) Health(_ context.Context) (*service.HealthStatus, error) {
	return nil, errors.New("health check failed")
}

func (e *errorAdminService) Status(_ context.Context) (*service.StatusInfo, error) {
	return nil, errors.New("status unavailable")
}

func (e *errorAdminService) Peers(_ context.Context) (*service.PeersInfo, error) {
	return nil, errors.New("peers unavailable")
}

func (e *errorAdminService) AddMember(_ context.Context, _, _ uint64, _ string) error {
	return &service.MembershipError{Op: "add", Err: errors.New("not leader")}
}

func (e *errorAdminService) RemoveMember(_ context.Context, _, _ uint64) error {
	return &service.MembershipError{Op: "remove", Err: errors.New("not leader")}
}

func (e *errorAdminService) RequestSnapshot(_ context.Context, _ uint64) error {
	return &service.SnapshotRequestError{Err: errors.New("no shards")}
}

func (e *errorAdminService) Drain(_ context.Context) error {
	return &service.DrainError{Err: errors.New("already draining")}
}

func (e *errorAdminService) Capabilities(_ context.Context) (*capabilities.Capabilities, error) {
	return nil, errors.New("capabilities unavailable")
}

func (e *errorAdminService) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	return errors.New("start shard unavailable")
}

func (e *errorAdminService) StopShard(_ context.Context, _ uint64) error {
	return errors.New("stop shard unavailable")
}

func (e *errorAdminService) GetShardState(_ context.Context, _ uint64) (*service.ShardStateResponse, error) {
	return nil, errors.New("get shard state unavailable")
}

func (e *errorAdminService) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	return nil, errors.New("get topology unavailable")
}

// yamlBrokenMarshaler implements yaml.Marshaler to trigger a marshal
// error through the standard interface instead of a panic.
type yamlBrokenMarshaler struct{}

func (y yamlBrokenMarshaler) MarshalYAML() (any, error) {
	return nil, errors.New("forced yaml marshal error")
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestRootCmd creates a root command wired to embedded mock services,
// bypassing the real PersistentPreRunE client construction.
func newTestRootCmd(kvSvc service.KVServicer, adminSvc service.AdminServicer) *cobra.Command {
	rootCmd := newRootCmd()

	// Override PersistentPreRunE to inject mock client.
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Name() == "version" || cmd.Name() == "help" {
			return nil
		}

		format, _ := cmd.Root().PersistentFlags().GetString("output")
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format

		t, err := embedded.New(kvSvc, adminSvc)
		if err != nil {
			return err
		}

		c, err := sdk.NewClient(t)
		if err != nil {
			return err
		}

		if err := c.Connect(cmd.Context()); err != nil {
			return err
		}

		client = c
		return nil
	}

	return rootCmd
}

// executeCommand runs a cobra command with the given args and captures
// the output to a buffer. Returns the captured output and any error.
// It saves and restores package-level state (outputWriter, outputFormat, client)
// so tests do not leak state into subsequent tests.
func executeCommand(root *cobra.Command, args ...string) (string, error) {
	var buf bytes.Buffer
	savedWriter := outputWriter
	savedFormat := outputFormat
	savedClient := client
	outputWriter = &buf
	defer func() {
		outputWriter = savedWriter
		outputFormat = savedFormat
		client = savedClient
	}()

	root.SetArgs(args)
	root.SetOut(&buf)
	root.SetErr(&buf)

	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

// ---------------------------------------------------------------------------
// KV command tests
// ---------------------------------------------------------------------------

func TestKVPut(t *testing.T) {
	kvSvc := newMockKVService()
	root := newTestRootCmd(kvSvc, newMockAdminService())

	out, err := executeCommand(root, "kv", "put", "hello", "world")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
	if string(kvSvc.store["hello"]) != "world" {
		t.Fatalf("expected stored value 'world', got: %q", kvSvc.store["hello"])
	}
}

func TestKVPutMissingArgs(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	_, err := executeCommand(root, "kv", "put", "onlykey")
	if err == nil {
		t.Fatal("expected error for missing value argument")
	}
}

func TestKVPutError(t *testing.T) {
	root := newTestRootCmd(&errorKVService{}, newMockAdminService())

	_, err := executeCommand(root, "kv", "put", "key", "value")
	if err == nil {
		t.Fatal("expected error from KV put")
	}
}

func TestKVGet(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["greeting"] = []byte("hello")
	root := newTestRootCmd(kvSvc, newMockAdminService())

	out, err := executeCommand(root, "kv", "get", "greeting")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("expected 'hello' in output, got: %q", out)
	}
}

func TestKVGetNotFound(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	_, err := executeCommand(root, "kv", "get", "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected KeyNotFoundError, got: %T: %v", err, err)
	}
}

func TestKVGetJSON(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["key1"] = []byte("value1")
	root := newTestRootCmd(kvSvc, newMockAdminService())

	out, err := executeCommand(root, "--output", "json", "kv", "get", "key1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"value1"`) {
		t.Fatalf("expected JSON quoted value, got: %q", out)
	}
}

func TestKVGetYAML(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["key1"] = []byte("value1")
	root := newTestRootCmd(kvSvc, newMockAdminService())

	out, err := executeCommand(root, "--output", "yaml", "kv", "get", "key1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "value1") {
		t.Fatalf("expected YAML value, got: %q", out)
	}
}

func TestKVGetError(t *testing.T) {
	root := newTestRootCmd(&errorKVService{}, newMockAdminService())

	_, err := executeCommand(root, "kv", "get", "anykey")
	if err == nil {
		t.Fatal("expected error from KV get")
	}
}

func TestKVDel(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["todelete"] = []byte("val")
	root := newTestRootCmd(kvSvc, newMockAdminService())

	out, err := executeCommand(root, "kv", "del", "todelete")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
	if _, exists := kvSvc.store["todelete"]; exists {
		t.Fatal("expected key to be deleted")
	}
}

func TestKVDelError(t *testing.T) {
	root := newTestRootCmd(&errorKVService{}, newMockAdminService())

	_, err := executeCommand(root, "kv", "del", "anykey")
	if err == nil {
		t.Fatal("expected error from KV delete")
	}
}

func TestKVList(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["app/key1"] = []byte("v1")
	kvSvc.store["app/key2"] = []byte("v2")
	root := newTestRootCmd(kvSvc, newMockAdminService())

	out, err := executeCommand(root, "kv", "list", "app/")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "app/key1") || !strings.Contains(out, "app/key2") {
		t.Fatalf("expected keys in output, got: %q", out)
	}
}

func TestKVListNoPrefix(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["a"] = []byte("1")
	kvSvc.store["b"] = []byte("2")
	root := newTestRootCmd(kvSvc, newMockAdminService())

	out, err := executeCommand(root, "kv", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "a") || !strings.Contains(out, "b") {
		t.Fatalf("expected all keys in output, got: %q", out)
	}
}

func TestKVListJSON(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["x"] = []byte("1")
	root := newTestRootCmd(kvSvc, newMockAdminService())

	out, err := executeCommand(root, "--output", "json", "kv", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "[") {
		t.Fatalf("expected JSON array, got: %q", out)
	}
}

func TestKVListYAML(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["x"] = []byte("1")
	root := newTestRootCmd(kvSvc, newMockAdminService())

	out, err := executeCommand(root, "--output", "yaml", "kv", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "- x") {
		t.Fatalf("expected YAML list, got: %q", out)
	}
}

func TestKVListError(t *testing.T) {
	root := newTestRootCmd(&errorKVService{}, newMockAdminService())

	_, err := executeCommand(root, "kv", "list")
	if err == nil {
		t.Fatal("expected error from KV list")
	}
}

func TestKVExistsTrue(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["present"] = []byte("yes")
	root := newTestRootCmd(kvSvc, newMockAdminService())

	out, err := executeCommand(root, "kv", "exists", "present")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "true") {
		t.Fatalf("expected 'true', got: %q", out)
	}
}

func TestKVExistsFalse(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "kv", "exists", "missing")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "false") {
		t.Fatalf("expected 'false', got: %q", out)
	}
}

func TestKVExistsError(t *testing.T) {
	root := newTestRootCmd(&errorKVService{}, newMockAdminService())

	_, err := executeCommand(root, "kv", "exists", "anykey")
	if err == nil {
		t.Fatal("expected error from KV exists")
	}
}

// ---------------------------------------------------------------------------
// Top-level alias tests
// ---------------------------------------------------------------------------

func TestPutAlias(t *testing.T) {
	kvSvc := newMockKVService()
	root := newTestRootCmd(kvSvc, newMockAdminService())

	out, err := executeCommand(root, "put", "aliaskey", "aliasval")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK, got: %q", out)
	}
	if string(kvSvc.store["aliaskey"]) != "aliasval" {
		t.Fatal("put alias did not store value")
	}
}

func TestGetAlias(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["akey"] = []byte("aval")
	root := newTestRootCmd(kvSvc, newMockAdminService())

	out, err := executeCommand(root, "get", "akey")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "aval") {
		t.Fatalf("expected value, got: %q", out)
	}
}

func TestDelAlias(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["dkey"] = []byte("dval")
	root := newTestRootCmd(kvSvc, newMockAdminService())

	out, err := executeCommand(root, "del", "dkey")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// Admin command tests
// ---------------------------------------------------------------------------

func TestAdminHealth(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "admin", "health")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("expected 'ok' in output, got: %q", out)
	}
}

func TestAdminHealthJSON(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "--output", "json", "admin", "health")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"status"`) {
		t.Fatalf("expected JSON status field, got: %q", out)
	}
}

func TestAdminHealthYAML(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "--output", "yaml", "admin", "health")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "status:") {
		t.Fatalf("expected YAML status field, got: %q", out)
	}
}

func TestAdminHealthError(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), &errorAdminService{})

	_, err := executeCommand(root, "admin", "health")
	if err == nil {
		t.Fatal("expected error from admin health")
	}
}

func TestAdminStatus(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "admin", "status")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "node-1") {
		t.Fatalf("expected node-1 in output, got: %q", out)
	}
}

func TestAdminStatusJSON(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "--output", "json", "admin", "status")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"node_id"`) {
		t.Fatalf("expected JSON node_id, got: %q", out)
	}
}

func TestAdminStatusYAML(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "--output", "yaml", "admin", "status")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "node-1") {
		t.Fatalf("expected node-1 in YAML output, got: %q", out)
	}
}

func TestAdminStatusError(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), &errorAdminService{})

	_, err := executeCommand(root, "admin", "status")
	if err == nil {
		t.Fatal("expected error from admin status")
	}
}

func TestAdminPeers(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "admin", "peers")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "127.0.0.1:63100") {
		t.Fatalf("expected peer address in output, got: %q", out)
	}
}

func TestAdminPeersJSON(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "--output", "json", "admin", "peers")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"shards"`) {
		t.Fatalf("expected JSON shards field, got: %q", out)
	}
}

func TestAdminPeersYAML(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "--output", "yaml", "admin", "peers")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "shards:") {
		t.Fatalf("expected YAML shards, got: %q", out)
	}
}

func TestAdminPeersError(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), &errorAdminService{})

	_, err := executeCommand(root, "admin", "peers")
	if err == nil {
		t.Fatal("expected error from admin peers")
	}
}

func TestAdminMemberAdd(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "admin", "member", "add", "--shard-id", "0", "--replica-id", "3", "127.0.0.1:63102")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK, got: %q", out)
	}
}

func TestAdminMemberAddMissingFlags(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	// Missing --shard-id and --replica-id required flags.
	_, err := executeCommand(root, "admin", "member", "add", "127.0.0.1:63102")
	if err == nil {
		t.Fatal("expected error for missing required flags")
	}
}

func TestAdminMemberAddError(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), &errorAdminService{})

	_, err := executeCommand(root, "admin", "member", "add", "--shard-id", "0", "--replica-id", "3", "127.0.0.1:63102")
	if err == nil {
		t.Fatal("expected error from admin member add")
	}
}

func TestAdminMemberRemove(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "admin", "member", "remove", "--shard-id", "0", "2")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK, got: %q", out)
	}
}

func TestAdminMemberRemoveInvalidReplicaID(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	_, err := executeCommand(root, "admin", "member", "remove", "--shard-id", "0", "notanumber")
	if err == nil {
		t.Fatal("expected error for invalid replica ID")
	}
	var ire *InvalidReplicaIDError
	if !errors.As(err, &ire) {
		t.Fatalf("expected InvalidReplicaIDError, got: %T: %v", err, err)
	}
}

func TestAdminMemberRemoveMissingShardFlag(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	_, err := executeCommand(root, "admin", "member", "remove", "2")
	if err == nil {
		t.Fatal("expected error for missing --shard-id flag")
	}
}

func TestAdminMemberRemoveError(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), &errorAdminService{})

	_, err := executeCommand(root, "admin", "member", "remove", "--shard-id", "0", "2")
	if err == nil {
		t.Fatal("expected error from admin member remove")
	}
}

func TestAdminSnapshot(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "admin", "snapshot", "--shard-id", "0")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK, got: %q", out)
	}
}

func TestAdminSnapshotMissingFlag(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	_, err := executeCommand(root, "admin", "snapshot")
	if err == nil {
		t.Fatal("expected error for missing --shard-id flag")
	}
}

func TestAdminSnapshotError(t *testing.T) {
	adminSvc := &mockAdminService{snapshotErr: &service.SnapshotRequestError{Err: errors.New("no shards")}}
	root := newTestRootCmd(newMockKVService(), adminSvc)

	_, err := executeCommand(root, "admin", "snapshot", "--shard-id", "0")
	if err == nil {
		t.Fatal("expected error from admin snapshot")
	}
}

func TestAdminDrain(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "admin", "drain")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK, got: %q", out)
	}
}

func TestAdminDrainError(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), &errorAdminService{})

	_, err := executeCommand(root, "admin", "drain")
	if err == nil {
		t.Fatal("expected error from admin drain")
	}
}

// ---------------------------------------------------------------------------
// Version command tests
// ---------------------------------------------------------------------------

func TestVersion(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	out, err := executeCommand(root, "version")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "QRDB CLI") {
		t.Fatalf("expected version header, got: %q", out)
	}
	if !strings.Contains(out, "commit:") {
		t.Fatalf("expected commit info, got: %q", out)
	}
	if !strings.Contains(out, "built:") {
		t.Fatalf("expected build date, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// Output format tests
// ---------------------------------------------------------------------------

func TestInvalidOutputFormat(t *testing.T) {
	root := newTestRootCmd(newMockKVService(), newMockAdminService())

	_, err := executeCommand(root, "--output", "xml", "kv", "list")
	if err == nil {
		t.Fatal("expected error for invalid output format")
	}
	var ofe *OutputFormatError
	if !errors.As(err, &ofe) {
		t.Fatalf("expected OutputFormatError, got: %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// resolveTransport tests
// ---------------------------------------------------------------------------

func TestResolveTransportExplicit(t *testing.T) {
	tests := []struct {
		name          string
		serverFlag    string
		transportFlag string
		wantType      string
		wantAddr      string
	}{
		{
			name:          "explicit grpc with address",
			serverFlag:    "myhost:1234",
			transportFlag: "grpc",
			wantType:      "grpc",
			wantAddr:      "myhost:1234",
		},
		{
			name:          "explicit rest with address",
			serverFlag:    "https://myhost:1234",
			transportFlag: "rest",
			wantType:      "rest",
			wantAddr:      "https://myhost:1234",
		},
		{
			name:          "explicit unix with address",
			serverFlag:    "/tmp/test.sock",
			transportFlag: "unix",
			wantType:      "unix",
			wantAddr:      "/tmp/test.sock",
		},
		{
			name:          "explicit mcp with address",
			serverFlag:    "http://localhost:63004/mcp",
			transportFlag: "mcp",
			wantType:      "mcp",
			wantAddr:      "http://localhost:63004/mcp",
		},
		{
			name:          "explicit grpc no address defaults",
			serverFlag:    "",
			transportFlag: "grpc",
			wantType:      "grpc",
			wantAddr:      defaultGRPCAddress,
		},
		{
			name:          "explicit unix no address defaults",
			serverFlag:    "",
			transportFlag: "unix",
			wantType:      "unix",
			wantAddr:      defaultUnixSocket,
		},
		{
			name:          "explicit rest no address defaults",
			serverFlag:    "",
			transportFlag: "rest",
			wantType:      "rest",
			wantAddr:      defaultGRPCAddress,
		},
		{
			name:          "explicit quic with address",
			serverFlag:    "myhost:63004",
			transportFlag: "quic",
			wantType:      "quic",
			wantAddr:      "myhost:63004",
		},
		{
			name:          "explicit quic no address defaults",
			serverFlag:    "",
			transportFlag: "quic",
			wantType:      "quic",
			wantAddr:      defaultQUICAddress,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotAddr := resolveTransport(tt.serverFlag, tt.transportFlag)
			if gotType != tt.wantType {
				t.Errorf("transport type = %q, want %q", gotType, tt.wantType)
			}
			if gotAddr != tt.wantAddr {
				t.Errorf("address = %q, want %q", gotAddr, tt.wantAddr)
			}
		})
	}
}

func TestResolveTransportAutoFromScheme(t *testing.T) {
	tests := []struct {
		name     string
		server   string
		wantType string
		wantAddr string
	}{
		{
			name:     "unix:// scheme",
			server:   "unix:///var/run/custom.sock",
			wantType: "unix",
			wantAddr: "/var/run/custom.sock",
		},
		{
			name:     "http:// scheme",
			server:   "http://localhost:8080",
			wantType: "rest",
			wantAddr: "http://localhost:8080",
		},
		{
			name:     "https:// scheme",
			server:   "https://myhost:443",
			wantType: "rest",
			wantAddr: "https://myhost:443",
		},
		{
			name:     "bare host:port defaults to grpc",
			server:   "myhost:9999",
			wantType: "grpc",
			wantAddr: "myhost:9999",
		},
		{
			name:     "quic:// scheme",
			server:   "quic://myhost:63004",
			wantType: "quic",
			wantAddr: "myhost:63004",
		},
		{
			name:     "quics:// scheme",
			server:   "quics://myhost:63004",
			wantType: "quic",
			wantAddr: "myhost:63004",
		},
		{
			name:     "grpcs:// scheme",
			server:   "grpcs://myhost:63003",
			wantType: "grpc",
			wantAddr: "myhost:63003",
		},
		{
			name:     "grpc:// scheme",
			server:   "grpc://myhost:63003",
			wantType: "grpc",
			wantAddr: "myhost:63003",
		},
		{
			name:     "mcp:// scheme",
			server:   "mcp://myhost:63005",
			wantType: "mcp",
			wantAddr: "https://myhost:63005/mcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotAddr := resolveTransport(tt.server, "")
			if gotType != tt.wantType {
				t.Errorf("transport type = %q, want %q", gotType, tt.wantType)
			}
			if gotAddr != tt.wantAddr {
				t.Errorf("address = %q, want %q", gotAddr, tt.wantAddr)
			}
		})
	}
}

func TestResolveTransportDefaultFallback(t *testing.T) {
	// When no flags and no Unix socket exists, should default to gRPC.
	gotType, gotAddr := resolveTransport("", "")
	// The default Unix socket likely does not exist in CI, so we expect gRPC.
	if gotType != transportGRPC && gotType != transportUnix {
		t.Errorf("expected grpc or unix, got: %q", gotType)
	}
	if gotType == transportGRPC && gotAddr != defaultGRPCAddress {
		t.Errorf("expected default gRPC address, got: %q", gotAddr)
	}
	if gotType == transportUnix && gotAddr != defaultUnixSocket {
		t.Errorf("expected default unix socket, got: %q", gotAddr)
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestTransportErrorFormatting(t *testing.T) {
	err := &TransportError{Transport: "grpc", Err: errors.New("dial failed")}
	expected := "qrdb: transport grpc failed: dial failed"
	if err.Error() != expected {
		t.Errorf("got %q, want %q", err.Error(), expected)
	}
	if err.Unwrap().Error() != "dial failed" {
		t.Error("Unwrap did not return underlying error")
	}
}

func TestConnectErrorFormatting(t *testing.T) {
	err := &ConnectError{Address: "localhost:1234", Err: errors.New("refused")}
	expected := "qrdb: connect to localhost:1234 failed: refused"
	if err.Error() != expected {
		t.Errorf("got %q, want %q", err.Error(), expected)
	}
	if err.Unwrap().Error() != "refused" {
		t.Error("Unwrap did not return underlying error")
	}
}

func TestOutputFormatErrorFormatting(t *testing.T) {
	err := &OutputFormatError{Format: "xml"}
	expected := "qrdb: unknown output format: xml"
	if err.Error() != expected {
		t.Errorf("got %q, want %q", err.Error(), expected)
	}
}

func TestMissingArgErrorFormatting(t *testing.T) {
	err := &MissingArgError{Arg: "key"}
	expected := "qrdb: missing required argument: key"
	if err.Error() != expected {
		t.Errorf("got %q, want %q", err.Error(), expected)
	}
}

func TestInvalidArgErrorFormatting(t *testing.T) {
	err := &InvalidArgError{Arg: "shard-id", Reason: "must be a positive integer"}
	expected := "qrdb: invalid argument shard-id: must be a positive integer"
	if err.Error() != expected {
		t.Errorf("got %q, want %q", err.Error(), expected)
	}
}

func TestInvalidReplicaIDErrorFormatting(t *testing.T) {
	err := &InvalidReplicaIDError{Input: "abc"}
	expected := "invalid replica ID: abc: must be a positive integer"
	if err.Error() != expected {
		t.Errorf("got %q, want %q", err.Error(), expected)
	}
}

// ---------------------------------------------------------------------------
// Output helper tests
// ---------------------------------------------------------------------------

func TestPrintJSON(t *testing.T) {
	var buf bytes.Buffer
	outputWriter = &buf

	err := printJSON(map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("printJSON error: %v", err)
	}
	if !strings.Contains(buf.String(), `"key": "value"`) {
		t.Fatalf("expected JSON object, got: %q", buf.String())
	}
}

func TestPrintYAML(t *testing.T) {
	var buf bytes.Buffer
	outputWriter = &buf

	err := printYAML(map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("printYAML error: %v", err)
	}
	if !strings.Contains(buf.String(), "key: value") {
		t.Fatalf("expected YAML, got: %q", buf.String())
	}
}

func TestPrintTable(t *testing.T) {
	var buf bytes.Buffer
	outputWriter = &buf

	printTable([]string{"NAME", "VALUE"}, [][]string{{"key1", "val1"}, {"key2", "val2"}})
	out := buf.String()
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "key1") {
		t.Fatalf("expected table output, got: %q", out)
	}
}

func TestPrintValue(t *testing.T) {
	var buf bytes.Buffer
	outputWriter = &buf

	printValue("hello")
	if !strings.Contains(buf.String(), "hello\n") {
		t.Fatalf("expected 'hello', got: %q", buf.String())
	}
}

func TestPrintOK(t *testing.T) {
	var buf bytes.Buffer
	outputWriter = &buf

	printOK()
	if !strings.Contains(buf.String(), "OK\n") {
		t.Fatalf("expected 'OK', got: %q", buf.String())
	}
}

func TestPrintListTable(t *testing.T) {
	var buf bytes.Buffer
	outputWriter = &buf

	err := printList([]string{"a", "b", "c"}, "table")
	if err != nil {
		t.Fatalf("printList error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "a\n") || !strings.Contains(out, "b\n") {
		t.Fatalf("expected list items, got: %q", out)
	}
}

func TestPrintListJSON(t *testing.T) {
	var buf bytes.Buffer
	outputWriter = &buf

	err := printList([]string{"x", "y"}, "json")
	if err != nil {
		t.Fatalf("printList error: %v", err)
	}
	if !strings.Contains(buf.String(), `"x"`) {
		t.Fatalf("expected JSON, got: %q", buf.String())
	}
}

func TestPrintListYAML(t *testing.T) {
	var buf bytes.Buffer
	outputWriter = &buf

	err := printList([]string{"x", "y"}, "yaml")
	if err != nil {
		t.Fatalf("printList error: %v", err)
	}
	if !strings.Contains(buf.String(), "- x") {
		t.Fatalf("expected YAML, got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// validateOutputFormat tests
// ---------------------------------------------------------------------------

func TestValidateOutputFormat(t *testing.T) {
	tests := []struct {
		format  string
		wantErr bool
	}{
		{"table", false},
		{"json", false},
		{"yaml", false},
		{"xml", true},
		{"", true},
		{"TABLE", true},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			err := validateOutputFormat(tt.format)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateOutputFormat(%q) error = %v, wantErr = %v", tt.format, err, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// newTransport tests
// ---------------------------------------------------------------------------

func TestNewTransportUnsupported(t *testing.T) {
	_, err := newTransport("invalid_transport")
	if err == nil {
		t.Fatal("expected error for unsupported transport")
	}
	var iae *InvalidArgError
	if !errors.As(err, &iae) {
		t.Fatalf("expected InvalidArgError, got: %T: %v", err, err)
	}
}

// TestNewTransportAllBuilders exercises each transport builder in the
// transportBuilders map to cover the success path of newTransport and
// every builder function body.
func TestNewTransportAllBuilders(t *testing.T) {
	tests := []struct {
		name          string
		transportType string
	}{
		{"rest builder", transportREST},
		{"grpc builder", transportGRPC},
		{"unix builder", transportUnix},
		{"mcp builder", transportMCP},
		{"quic builder", transportQUIC},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, err := newTransport(tt.transportType, transport.WithAddress("localhost:9999"))
			if err != nil {
				t.Fatalf("newTransport(%q) returned error: %v", tt.transportType, err)
			}
			if tr == nil {
				t.Fatalf("newTransport(%q) returned nil transport", tt.transportType)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// printJSON / printYAML error path tests
// ---------------------------------------------------------------------------

func TestPrintJSONMarshalError(t *testing.T) {
	var buf bytes.Buffer
	oldWriter := outputWriter
	outputWriter = &buf
	t.Cleanup(func() { outputWriter = oldWriter })

	// Channels are not JSON-serializable and trigger a marshal error.
	err := printJSON(make(chan int))
	if err == nil {
		t.Fatal("expected error from printJSON with unmarshalable value")
	}
}

func TestPrintYAMLMarshalError(t *testing.T) {
	var buf bytes.Buffer
	oldWriter := outputWriter
	outputWriter = &buf
	t.Cleanup(func() { outputWriter = oldWriter })

	// yamlBrokenMarshaler implements yaml.Marshaler and returns an error
	// through the standard interface, exercising the error return path
	// in printYAML without triggering a panic.
	err := printYAML(yamlBrokenMarshaler{})
	if err == nil {
		t.Fatal("expected error from printYAML with broken marshaler")
	}
	if !strings.Contains(err.Error(), "forced yaml marshal error") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Alias command nil-return path tests
// ---------------------------------------------------------------------------

// TestAliasCommandsNotFoundReturnsNil verifies that the alias functions
// return nil when the target subcommand name is not found in the kv
// command's child list. This covers the fallthrough return nil path.
func TestAliasCommandsNotFoundReturnsNil(t *testing.T) {
	emptyKVCmd := &cobra.Command{Use: "kv"}

	if result := newPutAliasCmd(emptyKVCmd); result != nil {
		t.Fatalf("newPutAliasCmd expected nil for empty kv cmd, got: %v", result)
	}
	if result := newGetAliasCmd(emptyKVCmd); result != nil {
		t.Fatalf("newGetAliasCmd expected nil for empty kv cmd, got: %v", result)
	}
	if result := newDelAliasCmd(emptyKVCmd); result != nil {
		t.Fatalf("newDelAliasCmd expected nil for empty kv cmd, got: %v", result)
	}
}

// ---------------------------------------------------------------------------
// resolveTransport additional coverage
// ---------------------------------------------------------------------------

// TestResolveTransportExplicitMCPNoAddress covers the default case in
// the switch that assigns defaultGRPCAddress when transport is "mcp"
// and no server address is provided.
func TestResolveTransportExplicitMCPNoAddress(t *testing.T) {
	gotType, gotAddr := resolveTransport("", "mcp")
	if gotType != "mcp" {
		t.Errorf("transport type = %q, want %q", gotType, "mcp")
	}
	if gotAddr != defaultGRPCAddress {
		t.Errorf("address = %q, want %q", gotAddr, defaultGRPCAddress)
	}
}

// ---------------------------------------------------------------------------
// Real PersistentPreRunE tests (using newRootCmd without override)
// ---------------------------------------------------------------------------

// TestRealPersistentPreRunE_VersionSkip exercises the real PersistentPreRunE
// from newRootCmd() with the "version" command, verifying the skip path
// (lines 49-51) where version/help bypass client construction.
func TestRealPersistentPreRunE_VersionSkip(t *testing.T) {
	root := newRootCmd()

	var buf bytes.Buffer
	oldWriter := outputWriter
	outputWriter = &buf
	t.Cleanup(func() { outputWriter = oldWriter })

	root.SetArgs([]string{"version"})
	root.SetOut(&buf)
	root.SetErr(&buf)

	err := root.ExecuteContext(context.Background())
	if err != nil {
		t.Fatalf("expected no error for version, got: %v", err)
	}
	if !strings.Contains(buf.String(), "QRDB CLI") {
		t.Fatalf("expected version output, got: %q", buf.String())
	}
}

// TestRealPersistentPreRunE_InvalidFormat exercises the real PersistentPreRunE
// with an invalid output format, which returns an OutputFormatError before
// reaching buildClient. This covers lines 54-57.
func TestRealPersistentPreRunE_InvalidFormat(t *testing.T) {
	root := newRootCmd()

	var buf bytes.Buffer
	oldWriter := outputWriter
	outputWriter = &buf
	t.Cleanup(func() { outputWriter = oldWriter })

	root.SetArgs([]string{"--output", "xml", "kv", "list"})
	root.SetOut(&buf)
	root.SetErr(&buf)

	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid output format")
	}
	var ofe *OutputFormatError
	if !errors.As(err, &ofe) {
		t.Fatalf("expected OutputFormatError, got: %T: %v", err, err)
	}
}

// TestRealPersistentPreRunE_BuildClientFailure exercises the real
// PersistentPreRunE through to buildClient, which fails because no
// server is running. This covers the format validation success path
// (lines 54-58), resolveTransport call (line 61), and the buildClient
// error return (lines 64-67).
func TestRealPersistentPreRunE_BuildClientFailure(t *testing.T) {
	root := newRootCmd()

	var buf bytes.Buffer
	oldWriter := outputWriter
	outputWriter = &buf
	t.Cleanup(func() { outputWriter = oldWriter })

	// Use a non-existent Unix socket to get a fast connection failure
	// instead of a slow gRPC dial timeout.
	root.SetArgs([]string{"--server", "unix:///tmp/nonexistent-qrdb-test.sock", "kv", "list"})
	root.SetOut(&buf)
	root.SetErr(&buf)

	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error from buildClient with no server")
	}
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*errorKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}
