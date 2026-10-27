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
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
)

// ---------------------------------------------------------------------------
// parsePeers tests
// ---------------------------------------------------------------------------

func TestParsePeers_SinglePeer(t *testing.T) {
	members, err := parsePeers([]string{"1=127.0.0.1:63001"})
	if err != nil {
		t.Fatalf("parsePeers() error: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("member count = %d, want 1", len(members))
	}
	addr, ok := members[1]
	if !ok {
		t.Fatal("member[1] not found")
	}
	if addr != "127.0.0.1:63001" {
		t.Fatalf("member[1] = %q, want %q", addr, "127.0.0.1:63001")
	}
}

func TestParsePeers_MultiplePeers(t *testing.T) {
	members, err := parsePeers([]string{"1=10.0.0.1:63001", "2=10.0.0.2:63001", "3=10.0.0.3:63001"})
	if err != nil {
		t.Fatalf("parsePeers() error: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("member count = %d, want 3", len(members))
	}
	if members[2] != "10.0.0.2:63001" {
		t.Fatalf("member[2] = %q, want %q", members[2], "10.0.0.2:63001")
	}
}

func TestParsePeers_EmptySlice(t *testing.T) {
	members, err := parsePeers(nil)
	if err != nil {
		t.Fatalf("parsePeers(nil) error: %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("member count = %d, want 0", len(members))
	}
}

func TestParsePeers_InvalidFormat_NoEquals(t *testing.T) {
	_, err := parsePeers([]string{"badformat"})
	if err == nil {
		t.Fatal("parsePeers() expected error for missing '='")
	}
	var parseErr *PeerParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error type = %T, want *PeerParseError", err)
	}
	if parseErr.Input != "badformat" {
		t.Fatalf("PeerParseError.Input = %q, want %q", parseErr.Input, "badformat")
	}
}

func TestParsePeers_InvalidID_NotNumeric(t *testing.T) {
	_, err := parsePeers([]string{"abc=127.0.0.1:63001"})
	if err == nil {
		t.Fatal("parsePeers() expected error for non-numeric ID")
	}
	var parseErr *PeerParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error type = %T, want *PeerParseError", err)
	}
}

func TestParsePeers_EmptyAddress(t *testing.T) {
	_, err := parsePeers([]string{"1="})
	if err == nil {
		t.Fatal("parsePeers() expected error for empty address")
	}
	var parseErr *PeerParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error type = %T, want *PeerParseError", err)
	}
}

func TestParsePeers_EqualsAtStart(t *testing.T) {
	_, err := parsePeers([]string{"=127.0.0.1:63001"})
	if err == nil {
		t.Fatal("parsePeers() expected error for '=' at start")
	}
	var parseErr *PeerParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error type = %T, want *PeerParseError", err)
	}
}

// ---------------------------------------------------------------------------
// isSingleNode tests
// ---------------------------------------------------------------------------

func TestIsSingleNode_StaticNoPeers(t *testing.T) {
	f := &startFlags{discoveryMethod: "static", peers: nil}
	if !isSingleNode(f) {
		t.Fatal("isSingleNode() = false, want true for static with no peers")
	}
}

func TestIsSingleNode_StaticOnePeer(t *testing.T) {
	f := &startFlags{discoveryMethod: "static", peers: []string{"1=127.0.0.1:63001"}}
	if !isSingleNode(f) {
		t.Fatal("isSingleNode() = false, want true for static with 1 peer")
	}
}

func TestIsSingleNode_StaticTwoPeers(t *testing.T) {
	f := &startFlags{
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001", "2=127.0.0.1:63002"},
	}
	if isSingleNode(f) {
		t.Fatal("isSingleNode() = true, want false for static with 2 peers")
	}
}

func TestIsSingleNode_NonStatic(t *testing.T) {
	f := &startFlags{discoveryMethod: "multicast", peers: nil}
	if isSingleNode(f) {
		t.Fatal("isSingleNode() = true, want false for multicast")
	}
}

func TestIsSingleNode_StaticInvalidPeer(t *testing.T) {
	// Invalid peer format should return false (parse error).
	f := &startFlags{discoveryMethod: "static", peers: []string{"bad-peer"}}
	if isSingleNode(f) {
		t.Fatal("isSingleNode() = true, want false for invalid peer format")
	}
}

// ---------------------------------------------------------------------------
// resolveCreateFunc tests
// ---------------------------------------------------------------------------

func TestResolveCreateFunc_Memory(t *testing.T) {
	fn, err := resolveCreateFunc("memory", "", statemachine.StorageConfig{})
	if err != nil {
		t.Fatalf("resolveCreateFunc(memory) error: %v", err)
	}
	if fn == nil {
		t.Fatal("resolveCreateFunc(memory) returned nil CreateFunc")
	}
}

func TestResolveCreateFunc_Concurrent(t *testing.T) {
	fn, err := resolveCreateFunc("concurrent", "", statemachine.StorageConfig{})
	if err != nil {
		t.Fatalf("resolveCreateFunc(concurrent) error: %v", err)
	}
	if fn == nil {
		t.Fatal("resolveCreateFunc(concurrent) returned nil CreateFunc")
	}
}

func TestResolveCreateFunc_Pebble(t *testing.T) {
	fn, err := resolveCreateFunc("pebble", "", statemachine.StorageConfig{})
	if err != nil {
		t.Fatalf("resolveCreateFunc(pebble) error: %v", err)
	}
	if fn == nil {
		t.Fatal("resolveCreateFunc(pebble) returned nil CreateFunc")
	}
}

func TestResolveCreateFunc_Unknown(t *testing.T) {
	_, err := resolveCreateFunc("unknown-type", "", statemachine.StorageConfig{})
	if err == nil {
		t.Fatal("resolveCreateFunc(unknown-type) expected error")
	}
	var smErr *UnknownSMError
	if !errors.As(err, &smErr) {
		t.Fatalf("error type = %T, want *UnknownSMError", err)
	}
	if smErr.Name != "unknown-type" {
		t.Fatalf("UnknownSMError.Name = %q, want %q", smErr.Name, "unknown-type")
	}
}

// ---------------------------------------------------------------------------
// buildHostConfig tests
// ---------------------------------------------------------------------------

func TestBuildHostConfig(t *testing.T) {
	f := &startFlags{
		dataDir:              "/tmp/data",
		raftAddress:          "10.0.0.1:63001",
		listen:               "0.0.0.0:63001",
		deploymentID:         42,
		maxTotalInMemLogSize: 1024 * 1024,
		logFormat:            "json",
		logLevel:             "debug",
	}

	cfg := buildHostConfig(f, nil)

	expectedWAL := filepath.Join("/tmp/data", "wal")
	expectedNH := filepath.Join("/tmp/data", "nodehost")

	if cfg.WALDir != expectedWAL {
		t.Fatalf("WALDir = %q, want %q", cfg.WALDir, expectedWAL)
	}
	if cfg.NodeHostDir != expectedNH {
		t.Fatalf("NodeHostDir = %q, want %q", cfg.NodeHostDir, expectedNH)
	}
	if cfg.RaftAddress != "10.0.0.1:63001" {
		t.Fatalf("RaftAddress = %q, want %q", cfg.RaftAddress, "10.0.0.1:63001")
	}
	if cfg.ListenAddress != "0.0.0.0:63001" {
		t.Fatalf("ListenAddress = %q, want %q", cfg.ListenAddress, "0.0.0.0:63001")
	}
	if cfg.DeploymentID != 42 {
		t.Fatalf("DeploymentID = %d, want %d", cfg.DeploymentID, 42)
	}
	if cfg.MaxTotalInMemLogSize != 1024*1024 {
		t.Fatalf("MaxTotalInMemLogSize = %d, want %d", cfg.MaxTotalInMemLogSize, 1024*1024)
	}
	if string(cfg.LogConfig.Format) != "json" {
		t.Fatalf("LogConfig.Format = %q, want %q", cfg.LogConfig.Format, "json")
	}
	if string(cfg.LogConfig.Level) != "debug" {
		t.Fatalf("LogConfig.Level = %q, want %q", cfg.LogConfig.Level, "debug")
	}
	if cfg.TransportConfig.MTLSConfig != nil {
		t.Fatal("TransportConfig.MTLSConfig should be nil when mtls param is nil")
	}
}

func TestBuildHostConfig_WithMTLS(t *testing.T) {
	f := &startFlags{
		dataDir:              "/tmp/data",
		raftAddress:          "10.0.0.1:63001",
		listen:               "0.0.0.0:63001",
		deploymentID:         42,
		maxTotalInMemLogSize: 1024 * 1024,
		logFormat:            "json",
		logLevel:             "debug",
	}

	mtls := &quicraftconfig.MTLSConfig{
		CACert: []byte("ca-cert-pem"),
		Cert:   []byte("node-cert-pem"),
		Key:    []byte("node-key-pem"),
	}

	cfg := buildHostConfig(f, mtls)

	if cfg.TransportConfig.MTLSConfig == nil {
		t.Fatal("TransportConfig.MTLSConfig is nil, want non-nil")
	}
	if string(cfg.TransportConfig.MTLSConfig.CACert) != "ca-cert-pem" {
		t.Fatalf("MTLSConfig.CACert = %q, want %q", cfg.TransportConfig.MTLSConfig.CACert, "ca-cert-pem")
	}
	if string(cfg.TransportConfig.MTLSConfig.Cert) != "node-cert-pem" {
		t.Fatalf("MTLSConfig.Cert = %q, want %q", cfg.TransportConfig.MTLSConfig.Cert, "node-cert-pem")
	}
	if string(cfg.TransportConfig.MTLSConfig.Key) != "node-key-pem" {
		t.Fatalf("MTLSConfig.Key = %q, want %q", cfg.TransportConfig.MTLSConfig.Key, "node-key-pem")
	}
}

// ---------------------------------------------------------------------------
// buildShardConfig tests
// ---------------------------------------------------------------------------

func TestBuildShardConfig(t *testing.T) {
	f := &startFlags{
		shardID:            5,
		replicaID:          3,
		maxInMemLogSize:    2048,
		snapshotEntries:    10000,
		compactionOverhead: 500,
	}

	cfg := buildShardConfig(f)

	if cfg.ShardID != 5 {
		t.Fatalf("ShardID = %d, want %d", cfg.ShardID, 5)
	}
	if cfg.ReplicaID != 3 {
		t.Fatalf("ReplicaID = %d, want %d", cfg.ReplicaID, 3)
	}
	if cfg.MaxInMemLogSize != 2048 {
		t.Fatalf("MaxInMemLogSize = %d, want %d", cfg.MaxInMemLogSize, 2048)
	}
	if cfg.SnapshotEntries != 10000 {
		t.Fatalf("SnapshotEntries = %d, want %d", cfg.SnapshotEntries, 10000)
	}
	if cfg.CompactionOverhead != 500 {
		t.Fatalf("CompactionOverhead = %d, want %d", cfg.CompactionOverhead, 500)
	}
}

// ---------------------------------------------------------------------------
// generateReplicaID tests
// ---------------------------------------------------------------------------

func TestGenerateReplicaID_NonZero(t *testing.T) {
	id, err := generateReplicaID()
	if err != nil {
		t.Fatalf("generateReplicaID() error: %v", err)
	}
	if id == 0 {
		t.Fatal("generateReplicaID() returned 0")
	}
}

func TestGenerateReplicaID_Unique(t *testing.T) {
	id1, err := generateReplicaID()
	if err != nil {
		t.Fatalf("generateReplicaID() first call error: %v", err)
	}
	id2, err := generateReplicaID()
	if err != nil {
		t.Fatalf("generateReplicaID() second call error: %v", err)
	}
	if id1 == id2 {
		t.Fatalf("generateReplicaID() returned identical values: %d", id1)
	}
}

func TestGenerateReplicaID_48BitMask(t *testing.T) {
	for i := 0; i < 100; i++ {
		id, err := generateReplicaID()
		if err != nil {
			t.Fatalf("generateReplicaID() error: %v", err)
		}
		if id > 0xFFFFFFFFFFFF {
			t.Fatalf("generateReplicaID() = %d, exceeds 48-bit mask", id)
		}
	}
}

// ---------------------------------------------------------------------------
// generateCATLS tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// buildServerConfig tests
// ---------------------------------------------------------------------------

func TestBuildServerConfig(t *testing.T) {
	f := &startFlags{
		adminPort:            8443,
		grpcPort:             8444,
		enableREST:           true,
		enableGRPC:           false,
		dataDir:              "/data/test",
		deploymentID:         7,
		shardID:              2,
		replicaID:            4,
		raftAddress:          "10.0.0.5:63001",
		listen:               "0.0.0.0:63001",
		smType:               "memory",
		maxInMemLogSize:      4096,
		maxTotalInMemLogSize: 8192,
		snapshotEntries:      5000,
		compactionOverhead:   1000,
		join:                 true,
		tlsClientAuth:        "require_and_verify",
		logLevel:             "warn",
		logFormat:            "json",
		discoveryMethod:      "static",
		multicastSecret:      "secret",
		dnsDomain:            "example.com",
		// The bind host has no default of its own inside startFlags:
		// cobra's --host default supplies it in production, so a
		// hand-built value has to state it too.
		host: qrdbconfig.DefaultServerHost,
	}

	cfg, cfgErr := buildServerConfig(f)
	if cfgErr != nil {
		t.Fatalf("buildServerConfig: %v", cfgErr)
	}

	if cfg.Server.AdminPort != 8443 {
		t.Fatalf("AdminPort = %d, want %d", cfg.Server.AdminPort, 8443)
	}
	if cfg.Server.GRPCPort != 8444 {
		t.Fatalf("GRPCPort = %d, want %d", cfg.Server.GRPCPort, 8444)
	}
	if cfg.Server.Host != qrdbconfig.DefaultServerHost {
		t.Fatalf("Host = %q, want %q", cfg.Server.Host, qrdbconfig.DefaultServerHost)
	}
	if !cfg.Protocols.REST {
		t.Fatal("Protocols.REST = false, want true")
	}
	if cfg.Protocols.GRPC {
		t.Fatal("Protocols.GRPC = true, want false")
	}
	if cfg.Raft.DataDir != "/data/test" {
		t.Fatalf("DataDir = %q, want %q", cfg.Raft.DataDir, "/data/test")
	}
	if cfg.Raft.DeploymentID != 7 {
		t.Fatalf("DeploymentID = %d, want %d", cfg.Raft.DeploymentID, 7)
	}
	if cfg.Raft.ShardID != 2 {
		t.Fatalf("ShardID = %d, want %d", cfg.Raft.ShardID, 2)
	}
	if cfg.Raft.ReplicaID != 4 {
		t.Fatalf("ReplicaID = %d, want %d", cfg.Raft.ReplicaID, 4)
	}
	if cfg.Raft.RaftAddress != "10.0.0.5:63001" {
		t.Fatalf("RaftAddress = %q, want %q", cfg.Raft.RaftAddress, "10.0.0.5:63001")
	}
	if cfg.Raft.ListenAddress != "0.0.0.0:63001" {
		t.Fatalf("ListenAddress = %q, want %q", cfg.Raft.ListenAddress, "0.0.0.0:63001")
	}
	if cfg.Raft.SMType != "memory" {
		t.Fatalf("SMType = %q, want %q", cfg.Raft.SMType, "memory")
	}
	if cfg.Raft.MaxInMemLogSize != 4096 {
		t.Fatalf("MaxInMemLogSize = %d, want %d", cfg.Raft.MaxInMemLogSize, 4096)
	}
	if cfg.Raft.MaxTotalInMemLogSize != 8192 {
		t.Fatalf("MaxTotalInMemLogSize = %d, want %d", cfg.Raft.MaxTotalInMemLogSize, 8192)
	}
	if cfg.Raft.SnapshotEntries != 5000 {
		t.Fatalf("SnapshotEntries = %d, want %d", cfg.Raft.SnapshotEntries, 5000)
	}
	if cfg.Raft.CompactionOverhead != 1000 {
		t.Fatalf("CompactionOverhead = %d, want %d", cfg.Raft.CompactionOverhead, 1000)
	}
	if !cfg.Raft.Join {
		t.Fatal("Raft.Join = false, want true")
	}
	if !cfg.TLS.Enabled {
		t.Fatal("TLS.Enabled = false, want true")
	}
	if cfg.TLS.ClientAuth != "require_and_verify" {
		t.Fatalf("TLS.ClientAuth = %q, want %q", cfg.TLS.ClientAuth, "require_and_verify")
	}
	if cfg.TLS.MinVersion != "TLS1.3" {
		t.Fatalf("TLS.MinVersion = %q, want %q", cfg.TLS.MinVersion, "TLS1.3")
	}
	if cfg.Logging.Level != "warn" {
		t.Fatalf("Logging.Level = %q, want %q", cfg.Logging.Level, "warn")
	}
	if cfg.Logging.Format != "json" {
		t.Fatalf("Logging.Format = %q, want %q", cfg.Logging.Format, "json")
	}
	if cfg.Discovery.Method != "static" {
		t.Fatalf("Discovery.Method = %q, want %q", cfg.Discovery.Method, "static")
	}
	if cfg.Discovery.MulticastSecret != "secret" {
		t.Fatalf("Discovery.MulticastSecret = %q, want %q", cfg.Discovery.MulticastSecret, "secret")
	}
	if cfg.Discovery.DNSDomain != "example.com" {
		t.Fatalf("Discovery.DNSDomain = %q, want %q", cfg.Discovery.DNSDomain, "example.com")
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestPeerParseError(t *testing.T) {
	err := &PeerParseError{Input: "badpeer"}
	want := `cluster: invalid peer format "badpeer", expected id=address`
	if err.Error() != want {
		t.Fatalf("PeerParseError.Error() = %q, want %q", err.Error(), want)
	}
}

func TestMissingFlagError(t *testing.T) {
	err := &MissingFlagError{Flag: "multicast-secret"}
	want := "cluster: required discovery input not set: multicast-secret"
	if err.Error() != want {
		t.Fatalf("MissingFlagError.Error() = %q, want %q", err.Error(), want)
	}
}

func TestNoPeersError(t *testing.T) {
	err := &NoPeersError{}
	want := "cluster: no peers configured for static discovery"
	if err.Error() != want {
		t.Fatalf("NoPeersError.Error() = %q, want %q", err.Error(), want)
	}
}

func TestUnknownSMError(t *testing.T) {
	err := &UnknownSMError{Name: "rocksdb"}
	want := `cli: unknown state machine type "rocksdb"`
	if err.Error() != want {
		t.Fatalf("UnknownSMError.Error() = %q, want %q", err.Error(), want)
	}
}

func TestUnknownDiscoveryError(t *testing.T) {
	err := &UnknownDiscoveryError{Name: "consul"}
	want := `cluster: unknown discovery method "consul"`
	if err.Error() != want {
		t.Fatalf("UnknownDiscoveryError.Error() = %q, want %q", err.Error(), want)
	}
}

func TestTLSGenerateError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("random source failed")
	err := &TLSGenerateError{Err: inner}
	want := "clustertls: TLS certificate generation failed: random source failed"
	if err.Error() != want {
		t.Fatalf("TLSGenerateError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("TLSGenerateError.Unwrap() did not return inner error")
	}
}

func TestTLSLoadError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("permission denied")
	err := &TLSLoadError{Path: "/etc/certs/ca.pem", Err: inner}
	want := `clustertls: failed to load TLS file "/etc/certs/ca.pem": permission denied`
	if err.Error() != want {
		t.Fatalf("TLSLoadError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("TLSLoadError.Unwrap() did not return inner error")
	}
}

// ---------------------------------------------------------------------------
// healthValue.String() test
// ---------------------------------------------------------------------------

func TestHealthValue_String(t *testing.T) {
	tests := []struct {
		val  healthValue
		want string
	}{
		{"healthy", "healthy"},
		{"no-leader", "no-leader"},
		{"lagging", "lagging"},
	}
	for _, tt := range tests {
		got := tt.val.String()
		if got != tt.want {
			t.Fatalf("healthValue(%q).String() = %q, want %q", tt.val, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// newVersionCmd test (smoke test)
// ---------------------------------------------------------------------------

func TestNewVersionCmd(t *testing.T) {
	cmd := newVersionCmd()
	if cmd.Use != "version" {
		t.Fatalf("Use = %q, want %q", cmd.Use, "version")
	}
}

// ---------------------------------------------------------------------------
// newStartCmd test (flag registration smoke test)
// ---------------------------------------------------------------------------

func TestNewStartCmd_DefaultFlags(t *testing.T) {
	cmd := newStartCmd()
	if cmd.Use != "start" {
		t.Fatalf("Use = %q, want %q", cmd.Use, "start")
	}

	// Verify a few flags are registered.
	flagNames := []string{
		"listen", "raft-address", "data-dir", "deployment-id",
		"sm", "discovery", "peers", "shard-id", "replica-id",
		"admin-port", "grpc-port", "config", "log-format", "log-level",
		"enable-rest", "enable-grpc",
	}
	for _, name := range flagNames {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("flag --%s not registered", name)
		}
	}
}

// ---------------------------------------------------------------------------
// newStatusCmd and newPeersCmd smoke tests
// ---------------------------------------------------------------------------

func TestNewStatusCmd(t *testing.T) {
	cmd := newStatusCmd()
	if cmd.Use != "status" {
		t.Fatalf("Use = %q, want %q", cmd.Use, "status")
	}
	f := cmd.Flags().Lookup("address")
	if f == nil {
		t.Fatal("flag --address not registered on status command")
	}
	if f.DefValue != "127.0.0.1:63002" {
		t.Fatalf("address default = %q, want %q", f.DefValue, "127.0.0.1:63002")
	}
}

func TestNewPeersCmd(t *testing.T) {
	cmd := newPeersCmd()
	if cmd.Use != "peers" {
		t.Fatalf("Use = %q, want %q", cmd.Use, "peers")
	}
	f := cmd.Flags().Lookup("address")
	if f == nil {
		t.Fatal("flag --address not registered on peers command")
	}
}

// ---------------------------------------------------------------------------
// smFactories and discoveryBuilders maps: ensure expected keys exist
// ---------------------------------------------------------------------------

func TestSMFactories_Keys(t *testing.T) {
	// The init() function in start.go registers "pebble" and "mvcc"
	// in the centralized statemachine registry.
	// The statemachine package itself registers "memory", "regular",
	// "concurrent", and "fast". Verify all expected types are
	// available after startup.
	expected := []string{"memory", "standard", "regular", "concurrent", "fast", "disk", "highperf", "pebble", "mvcc"}
	for _, key := range expected {
		if !statemachine.Has(key) {
			t.Fatalf("statemachine registry missing type %q", key)
		}
	}
}

// Discovery method builders and the minimum-peers default now live in
// pkg/cluster; their behavior is covered by that package's tests
// (TestBuildDiscovery_* and TestBootstrapShard_*).

// ---------------------------------------------------------------------------
// version variables test
// ---------------------------------------------------------------------------

func TestVersionVariables(t *testing.T) {
	if version == "" {
		t.Fatal("version should not be empty")
	}
	if gitCommit == "" {
		t.Fatal("gitCommit should not be empty")
	}
	if buildDate == "" {
		t.Fatal("buildDate should not be empty")
	}
}

// ---------------------------------------------------------------------------
// Mock types for testing
// ---------------------------------------------------------------------------

// mockRaftNode implements the raftNode interface for unit testing.
// testLeaderWait is the leader-wait budget tests hand runFirstBoot,
// bootstrapUserRoleStores and waitForLeaderElection. A mockRaftNode answers
// at once, so the budget matters only to a test whose node never names a
// leader; those pass their own, small, budget.
const testLeaderWait = 10 * time.Second

type mockRaftNode struct {
	startShardFn func(map[uint64]string, bool, sm.CreateFunc, quicraftconfig.Config) error
	getLeaderFn  func(uint64) (uint64, bool, error)
	closeErr     error
}

func (m *mockRaftNode) StartShard(members map[uint64]string, join bool, create sm.CreateFunc, cfg quicraftconfig.Config) error {
	if m.startShardFn != nil {
		return m.startShardFn(members, join, create, cfg)
	}
	return nil
}

func (m *mockRaftNode) LeaderID(shardID uint64) (uint64, bool, error) {
	if m.getLeaderFn != nil {
		return m.getLeaderFn(shardID)
	}
	return 1, true, nil
}

func (m *mockRaftNode) Close() error {
	return m.closeErr
}

// mockCloser implements io.Closer for testing shutdownNode.
type mockCloser struct {
	err error
}

func (m *mockCloser) Close() error {
	return m.err
}

// ---------------------------------------------------------------------------
// shutdownNode tests
// ---------------------------------------------------------------------------

func TestShutdownNode_Success(t *testing.T) {
	c := &mockCloser{err: nil}
	err := shutdownNode(c)
	if err != nil {
		t.Fatalf("shutdownNode() error = %v, want nil", err)
	}
}

func TestShutdownNode_Error(t *testing.T) {
	sentinel := errors.New("shutdown failed")
	c := &mockCloser{err: sentinel}
	err := shutdownNode(c)
	if err == nil {
		t.Fatal("shutdownNode() error = nil, want non-nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("shutdownNode() error = %v, want %v", err, sentinel)
	}
}

// ---------------------------------------------------------------------------
// applyConfigToFlags tests
// ---------------------------------------------------------------------------

func TestApplyConfigToFlags_AllFieldsFromConfig(t *testing.T) {
	cmd := newStartCmd()

	cfg := &qrdbconfig.Config{
		Server: qrdbconfig.ServerConfig{
			Host:      "10.0.0.5",
			AdminPort: 9443,
			GRPCPort:  9444,
		},
		Protocols: qrdbconfig.ProtocolsConfig{
			REST: false,
			GRPC: true,
		},
		Raft: qrdbconfig.RaftConfig{
			RaftAddress:          "10.0.0.5:63001",
			ListenAddress:        "0.0.0.0:63001",
			DataDir:              "/var/data/qrdb",
			DeploymentID:         42,
			SMType:               "memory",
			ShardID:              7,
			ReplicaID:            3,
			Peers:                "1=10.0.0.1:63001,2=10.0.0.2:63001",
			MaxInMemLogSize:      4096,
			MaxTotalInMemLogSize: 8192,
			SnapshotEntries:      10000,
			CompactionOverhead:   500,
			Join:                 true,
		},
		Logging: qrdbconfig.LoggingConfig{
			Format: "json",
			Level:  "debug",
		},
		Discovery: qrdbconfig.DiscoveryConfig{
			Method:          "dns",
			MulticastSecret: "testsecret",
			DNSDomain:       "example.com",
		},
	}

	f := &startFlags{host: testBindHost}
	applyConfigToFlags(cmd, f, cfg)

	// f.listen is the Raft host bind address, sourced from Raft.ListenAddress
	// (preferred) — NOT the admin HTTP port.
	if f.listen != "0.0.0.0:63001" {
		t.Fatalf("listen = %q, want %q", f.listen, "0.0.0.0:63001")
	}
	if f.raftAddress != "10.0.0.5:63001" {
		t.Fatalf("raftAddress = %q, want %q", f.raftAddress, "10.0.0.5:63001")
	}
	if f.dataDir != "/var/data/qrdb" {
		t.Fatalf("dataDir = %q, want %q", f.dataDir, "/var/data/qrdb")
	}
	if f.deploymentID != 42 {
		t.Fatalf("deploymentID = %d, want %d", f.deploymentID, 42)
	}
	if f.smType != "memory" {
		t.Fatalf("smType = %q, want %q", f.smType, "memory")
	}
	if f.discoveryMethod != "dns" {
		t.Fatalf("discoveryMethod = %q, want %q", f.discoveryMethod, "dns")
	}
	if len(f.peers) != 2 {
		t.Fatalf("peers length = %d, want 2", len(f.peers))
	}
	if f.peers[0] != "1=10.0.0.1:63001" {
		t.Fatalf("peers[0] = %q, want %q", f.peers[0], "1=10.0.0.1:63001")
	}
	if f.shardID != 7 {
		t.Fatalf("shardID = %d, want %d", f.shardID, 7)
	}
	if f.replicaID != 3 {
		t.Fatalf("replicaID = %d, want %d", f.replicaID, 3)
	}
	if f.multicastSecret != "testsecret" {
		t.Fatalf("multicastSecret = %q, want %q", f.multicastSecret, "testsecret")
	}
	if f.dnsDomain != "example.com" {
		t.Fatalf("dnsDomain = %q, want %q", f.dnsDomain, "example.com")
	}
	if f.adminPort != 9443 {
		t.Fatalf("adminPort = %d, want %d", f.adminPort, 9443)
	}
	if f.grpcPort != 9444 {
		t.Fatalf("grpcPort = %d, want %d", f.grpcPort, 9444)
	}
	if f.maxInMemLogSize != 4096 {
		t.Fatalf("maxInMemLogSize = %d, want %d", f.maxInMemLogSize, 4096)
	}
	if f.maxTotalInMemLogSize != 8192 {
		t.Fatalf("maxTotalInMemLogSize = %d, want %d", f.maxTotalInMemLogSize, 8192)
	}
	if f.snapshotEntries != 10000 {
		t.Fatalf("snapshotEntries = %d, want %d", f.snapshotEntries, 10000)
	}
	if f.compactionOverhead != 500 {
		t.Fatalf("compactionOverhead = %d, want %d", f.compactionOverhead, 500)
	}
	if !f.join {
		t.Fatal("join = false, want true")
	}
	if f.logFormat != "json" {
		t.Fatalf("logFormat = %q, want %q", f.logFormat, "json")
	}
	if f.logLevel != "debug" {
		t.Fatalf("logLevel = %q, want %q", f.logLevel, "debug")
	}
	if f.enableREST {
		t.Fatal("enableREST = true, want false")
	}
	if !f.enableGRPC {
		t.Fatal("enableGRPC = false, want true")
	}
}

func TestApplyConfigToFlags_CLIFlagsPrecedence(t *testing.T) {
	cmd := newStartCmd()

	// Simulate CLI flags being explicitly set by the user.
	if err := cmd.Flags().Set("listen", "0.0.0.0:12345"); err != nil {
		t.Fatalf("Set listen: %v", err)
	}
	if err := cmd.Flags().Set("raft-address", "192.168.1.1:12345"); err != nil {
		t.Fatalf("Set raft-address: %v", err)
	}
	if err := cmd.Flags().Set("data-dir", "/cli/data"); err != nil {
		t.Fatalf("Set data-dir: %v", err)
	}
	if err := cmd.Flags().Set("shard-id", "99"); err != nil {
		t.Fatalf("Set shard-id: %v", err)
	}
	if err := cmd.Flags().Set("log-level", "warn"); err != nil {
		t.Fatalf("Set log-level: %v", err)
	}
	if err := cmd.Flags().Set("enable-rest", "false"); err != nil {
		t.Fatalf("Set enable-rest: %v", err)
	}

	cfg := &qrdbconfig.Config{
		Server: qrdbconfig.ServerConfig{
			Host:      "10.0.0.5",
			AdminPort: 9443,
			GRPCPort:  9444,
		},
		Protocols: qrdbconfig.ProtocolsConfig{
			REST: true,
			GRPC: false,
		},
		Raft: qrdbconfig.RaftConfig{
			RaftAddress:  "10.0.0.5:63001",
			DataDir:      "/yaml/data",
			DeploymentID: 100,
			ShardID:      50,
			ReplicaID:    10,
		},
		Logging: qrdbconfig.LoggingConfig{
			Level:  "error",
			Format: "json",
		},
		Discovery: qrdbconfig.DiscoveryConfig{
			Method: "static",
		},
	}

	// Pre-populate f with the CLI values to mimic the real flow where
	// cobra binds the flags to the struct fields via StringVar/Uint64Var.
	f := &startFlags{
		listen:      "0.0.0.0:12345",
		raftAddress: "192.168.1.1:12345",
		dataDir:     "/cli/data",
		shardID:     99,
		logLevel:    "warn",
		enableREST:  false,
		host:        testBindHost,
	}
	applyConfigToFlags(cmd, f, cfg)

	// CLI flags (marked as Changed) should take precedence -- values
	// should remain as set on the struct.
	if f.listen != "0.0.0.0:12345" {
		t.Fatalf("listen = %q, want CLI value %q", f.listen, "0.0.0.0:12345")
	}
	if f.raftAddress != "192.168.1.1:12345" {
		t.Fatalf("raftAddress = %q, want CLI value %q", f.raftAddress, "192.168.1.1:12345")
	}
	if f.dataDir != "/cli/data" {
		t.Fatalf("dataDir = %q, want CLI value %q", f.dataDir, "/cli/data")
	}
	if f.shardID != 99 {
		t.Fatalf("shardID = %d, want CLI value 99", f.shardID)
	}
	if f.logLevel != "warn" {
		t.Fatalf("logLevel = %q, want CLI value %q", f.logLevel, "warn")
	}
	if f.enableREST {
		t.Fatal("enableREST = true, want CLI value false")
	}

	// Non-CLI flags (not marked Changed) should get YAML values.
	if f.deploymentID != 100 {
		t.Fatalf("deploymentID = %d, want YAML value 100", f.deploymentID)
	}
	if f.replicaID != 10 {
		t.Fatalf("replicaID = %d, want YAML value 10", f.replicaID)
	}
	if f.logFormat != "json" {
		t.Fatalf("logFormat = %q, want YAML value %q", f.logFormat, "json")
	}
	if f.discoveryMethod != "static" {
		t.Fatalf("discoveryMethod = %q, want YAML value %q", f.discoveryMethod, "static")
	}
}

func TestApplyConfigToFlags_EmptyPeersNotOverridden(t *testing.T) {
	cmd := newStartCmd()

	cfg := &qrdbconfig.Config{
		Raft: qrdbconfig.RaftConfig{
			Peers: "", // Empty peers in YAML.
		},
		Server: qrdbconfig.ServerConfig{Host: "0.0.0.0", AdminPort: 63002},
	}

	f := &startFlags{peers: []string{"1=127.0.0.1:63001"}, host: testBindHost}
	applyConfigToFlags(cmd, f, cfg)

	// Peers should remain unchanged because YAML peers is empty.
	if len(f.peers) != 1 {
		t.Fatalf("peers length = %d, want 1", len(f.peers))
	}
}

func TestApplyConfigToFlags_EmptySecretNotOverridden(t *testing.T) {
	cmd := newStartCmd()

	cfg := &qrdbconfig.Config{
		Discovery: qrdbconfig.DiscoveryConfig{
			MulticastSecret: "",
			DNSDomain:       "",
		},
		Server: qrdbconfig.ServerConfig{Host: "0.0.0.0", AdminPort: 63002},
	}

	f := &startFlags{multicastSecret: "original", dnsDomain: "original.com", host: testBindHost}
	applyConfigToFlags(cmd, f, cfg)

	// Empty YAML values should not override existing defaults.
	if f.multicastSecret != "original" {
		t.Fatalf("multicastSecret = %q, want %q", f.multicastSecret, "original")
	}
	if f.dnsDomain != "original.com" {
		t.Fatalf("dnsDomain = %q, want %q", f.dnsDomain, "original.com")
	}
}

// ---------------------------------------------------------------------------
// resolveAutoConfig tests
// ---------------------------------------------------------------------------

func TestResolveAutoConfig_StaticNoOp(t *testing.T) {
	cmd := newStartCmd()
	f := &startFlags{
		discoveryMethod: "static",
		listen:          "0.0.0.0:63001",
		raftAddress:     "127.0.0.1:63001",
		dataDir:         "./data",
		replicaID:       1,
		adminPort:       63002,
		grpcPort:        63003,
		host:            testBindHost,
	}

	err := resolveAutoConfig(cmd, f)
	if err != nil {
		t.Fatalf("resolveAutoConfig() error = %v, want nil", err)
	}

	// Static discovery should leave all values unchanged.
	if f.listen != "0.0.0.0:63001" {
		t.Fatalf("listen = %q, want %q", f.listen, "0.0.0.0:63001")
	}
	if f.raftAddress != "127.0.0.1:63001" {
		t.Fatalf("raftAddress = %q, want %q", f.raftAddress, "127.0.0.1:63001")
	}
	if f.dataDir != "./data" {
		t.Fatalf("dataDir = %q, want %q", f.dataDir, "./data")
	}
	if f.replicaID != 1 {
		t.Fatalf("replicaID = %d, want 1", f.replicaID)
	}
}

func TestResolveAutoConfig_NonStaticAutoGenerates(t *testing.T) {
	cmd := newStartCmd()
	f := &startFlags{
		discoveryMethod: "multicast",
		listen:          "0.0.0.0:63001",
		raftAddress:     "127.0.0.1:63001",
		dataDir:         "./data",
		replicaID:       1,
		adminPort:       63002,
		grpcPort:        63003,
		configFile:      "", // No config file, so auto-generation applies.
		host:            testBindHost,
	}

	err := resolveAutoConfig(cmd, f)
	if err != nil {
		t.Fatalf("resolveAutoConfig() error = %v, want nil", err)
	}

	// Replica ID should be auto-generated (non-1, non-zero).
	if f.replicaID == 1 || f.replicaID == 0 {
		t.Fatalf("replicaID = %d, want auto-generated non-default", f.replicaID)
	}

	// Listen and raftAddress should have new ports.
	if f.listen == "0.0.0.0:63001" {
		t.Fatal("listen should have been auto-changed from default")
	}
	if f.raftAddress == "127.0.0.1:63001" {
		t.Fatal("raftAddress should have been auto-changed from default")
	}

	// Admin port should differ from default.
	if f.adminPort == 63002 {
		t.Fatal("adminPort should have been auto-selected")
	}

	// gRPC port should differ from default.
	if f.grpcPort == 63003 {
		t.Fatal("grpcPort should have been auto-selected")
	}

	// Data dir should include replica ID.
	expected := fmt.Sprintf("./data/%d", f.replicaID)
	if f.dataDir != expected {
		t.Fatalf("dataDir = %q, want %q", f.dataDir, expected)
	}
}

func TestResolveAutoConfig_WithConfigFileSkips(t *testing.T) {
	cmd := newStartCmd()
	f := &startFlags{
		discoveryMethod: "multicast",
		listen:          "0.0.0.0:63001",
		raftAddress:     "127.0.0.1:63001",
		dataDir:         "./data",
		replicaID:       1,
		adminPort:       63002,
		grpcPort:        63003,
		configFile:      "/some/config.yaml", // Config file set, skip auto-gen.
		host:            testBindHost,
	}

	err := resolveAutoConfig(cmd, f)
	if err != nil {
		t.Fatalf("resolveAutoConfig() error = %v, want nil", err)
	}

	// With configFile set, auto-generation should be skipped.
	if f.replicaID != 1 {
		t.Fatalf("replicaID = %d, want 1 (no auto-generation with configFile)", f.replicaID)
	}
	if f.listen != "0.0.0.0:63001" {
		t.Fatalf("listen = %q, want unchanged", f.listen)
	}
	if f.adminPort != 63002 {
		t.Fatalf("adminPort = %d, want 63002 (unchanged)", f.adminPort)
	}
	if f.grpcPort != 63003 {
		t.Fatalf("grpcPort = %d, want 63003 (unchanged)", f.grpcPort)
	}
	if f.dataDir != "./data" {
		t.Fatalf("dataDir = %q, want unchanged", f.dataDir)
	}
}

func TestResolveAutoConfig_NonStaticWithExplicitFlags(t *testing.T) {
	cmd := newStartCmd()
	// Simulate explicit flag settings via cmd.Flags().Set.
	if err := cmd.Flags().Set("replica-id", "42"); err != nil {
		t.Fatalf("Set replica-id: %v", err)
	}
	if err := cmd.Flags().Set("listen", "0.0.0.0:50000"); err != nil {
		t.Fatalf("Set listen: %v", err)
	}
	if err := cmd.Flags().Set("raft-address", "10.0.0.1:50000"); err != nil {
		t.Fatalf("Set raft-address: %v", err)
	}
	if err := cmd.Flags().Set("admin-port", "50001"); err != nil {
		t.Fatalf("Set admin-port: %v", err)
	}
	if err := cmd.Flags().Set("grpc-port", "50002"); err != nil {
		t.Fatalf("Set grpc-port: %v", err)
	}
	if err := cmd.Flags().Set("data-dir", "/custom/data"); err != nil {
		t.Fatalf("Set data-dir: %v", err)
	}

	f := &startFlags{
		discoveryMethod: "multicast",
		listen:          "0.0.0.0:50000",
		raftAddress:     "10.0.0.1:50000",
		dataDir:         "/custom/data",
		replicaID:       42,
		adminPort:       50001,
		grpcPort:        50002,
	}

	err := resolveAutoConfig(cmd, f)
	if err != nil {
		t.Fatalf("resolveAutoConfig() error = %v, want nil", err)
	}

	// Explicitly set flags should remain unchanged.
	if f.replicaID != 42 {
		t.Fatalf("replicaID = %d, want 42 (explicitly set)", f.replicaID)
	}
	if f.listen != "0.0.0.0:50000" {
		t.Fatalf("listen = %q, want unchanged", f.listen)
	}
	if f.adminPort != 50001 {
		t.Fatalf("adminPort = %d, want 50001 (explicitly set)", f.adminPort)
	}
	if f.grpcPort != 50002 {
		t.Fatalf("grpcPort = %d, want 50002 (explicitly set)", f.grpcPort)
	}
	if f.dataDir != "/custom/data" {
		t.Fatalf("dataDir = %q, want unchanged (explicitly set)", f.dataDir)
	}
}

// ---------------------------------------------------------------------------
// startShard tests
// ---------------------------------------------------------------------------

func TestStartShard_StaticDelegation(t *testing.T) {
	var calledWithMembers map[uint64]string
	node := &mockRaftNode{
		startShardFn: func(members map[uint64]string, _ bool, _ sm.CreateFunc, _ quicraftconfig.Config) error {
			calledWithMembers = members
			return nil
		},
	}

	f := &startFlags{
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
	}

	err := startShard(context.Background(), f, node, nil, quicraftconfig.Config{})
	if err != nil {
		t.Fatalf("startShard() error = %v, want nil", err)
	}
	if calledWithMembers == nil {
		t.Fatal("startShardStatic was not called")
	}
	if len(calledWithMembers) != 1 {
		t.Fatalf("members count = %d, want 1", len(calledWithMembers))
	}
}

func TestStartShard_NonStaticDelegation(t *testing.T) {
	f := &startFlags{
		discoveryMethod: "unknown-method",
	}

	err := startShard(context.Background(), f, &mockRaftNode{}, nil, quicraftconfig.Config{})
	if err == nil {
		t.Fatal("startShard() error = nil, want UnknownDiscoveryError")
	}
	var ude *UnknownDiscoveryError
	if !errors.As(err, &ude) {
		t.Fatalf("error type = %T, want *UnknownDiscoveryError", err)
	}
	if ude.Name != "unknown-method" {
		t.Fatalf("UnknownDiscoveryError.Name = %q, want %q", ude.Name, "unknown-method")
	}
}

// ---------------------------------------------------------------------------
// startShardStatic tests
// ---------------------------------------------------------------------------

func TestStartShardStatic_Success(t *testing.T) {
	var receivedMembers map[uint64]string
	var receivedJoin bool
	node := &mockRaftNode{
		startShardFn: func(members map[uint64]string, join bool, _ sm.CreateFunc, _ quicraftconfig.Config) error {
			receivedMembers = members
			receivedJoin = join
			return nil
		},
	}

	f := &startFlags{
		peers: []string{"1=10.0.0.1:63001", "2=10.0.0.2:63001"},
		join:  true,
	}

	err := startShardStatic(f, node, nil, quicraftconfig.Config{})
	if err != nil {
		t.Fatalf("startShardStatic() error = %v, want nil", err)
	}
	if len(receivedMembers) != 2 {
		t.Fatalf("members count = %d, want 2", len(receivedMembers))
	}
	if !receivedJoin {
		t.Fatal("join = false, want true")
	}
}

func TestStartShardStatic_NoPeers(t *testing.T) {
	node := &mockRaftNode{}
	f := &startFlags{peers: nil}

	err := startShardStatic(f, node, nil, quicraftconfig.Config{})
	if err == nil {
		t.Fatal("startShardStatic() error = nil, want NoPeersError")
	}
	var npe *NoPeersError
	if !errors.As(err, &npe) {
		t.Fatalf("error type = %T, want *NoPeersError", err)
	}
}

func TestStartShardStatic_InvalidPeers(t *testing.T) {
	node := &mockRaftNode{}
	f := &startFlags{peers: []string{"badformat"}}

	err := startShardStatic(f, node, nil, quicraftconfig.Config{})
	if err == nil {
		t.Fatal("startShardStatic() error = nil, want PeerParseError")
	}
	var ppe *PeerParseError
	if !errors.As(err, &ppe) {
		t.Fatalf("error type = %T, want *PeerParseError", err)
	}
}

func TestStartShardStatic_NodeError(t *testing.T) {
	sentinel := errors.New("shard start failed")
	node := &mockRaftNode{
		startShardFn: func(_ map[uint64]string, _ bool, _ sm.CreateFunc, _ quicraftconfig.Config) error {
			return sentinel
		},
	}

	f := &startFlags{
		peers: []string{"1=127.0.0.1:63001"},
	}

	err := startShardStatic(f, node, nil, quicraftconfig.Config{})
	if err == nil {
		t.Fatal("startShardStatic() error = nil, want non-nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want %v", err, sentinel)
	}
}

// ---------------------------------------------------------------------------
// startShardWithBootstrap tests
// ---------------------------------------------------------------------------

func TestStartShardWithBootstrap_UnknownDiscovery(t *testing.T) {
	node := &mockRaftNode{}
	f := &startFlags{discoveryMethod: "consul"}

	err := startShardWithBootstrap(context.Background(), f, node, nil, quicraftconfig.Config{})
	if err == nil {
		t.Fatal("startShardWithBootstrap() error = nil, want UnknownDiscoveryError")
	}
	var ude *UnknownDiscoveryError
	if !errors.As(err, &ude) {
		t.Fatalf("error type = %T, want *UnknownDiscoveryError", err)
	}
	if ude.Name != "consul" {
		t.Fatalf("UnknownDiscoveryError.Name = %q, want %q", ude.Name, "consul")
	}
}

func TestStartShardWithBootstrap_DNSMissingDomain(t *testing.T) {
	node := &mockRaftNode{}
	f := &startFlags{
		discoveryMethod: "dns",
		dnsDomain:       "", // Missing required domain.
	}

	err := startShardWithBootstrap(context.Background(), f, node, nil, quicraftconfig.Config{})
	if err == nil {
		t.Fatal("startShardWithBootstrap() error = nil, want MissingFlagError")
	}
	var mfe *MissingFlagError
	if !errors.As(err, &mfe) {
		t.Fatalf("error type = %T, want *MissingFlagError", err)
	}
	if mfe.Flag != "dns-domain" {
		t.Fatalf("MissingFlagError.Flag = %q, want %q", mfe.Flag, "dns-domain")
	}
}

func TestStartShardWithBootstrap_MulticastMissingSecret(t *testing.T) {
	node := &mockRaftNode{}
	f := &startFlags{
		discoveryMethod: "multicast",
		multicastSecret: "", // Missing required secret.
	}

	err := startShardWithBootstrap(context.Background(), f, node, nil, quicraftconfig.Config{})
	if err == nil {
		t.Fatal("startShardWithBootstrap() error = nil, want MissingFlagError")
	}
	var mfe *MissingFlagError
	if !errors.As(err, &mfe) {
		t.Fatalf("error type = %T, want *MissingFlagError", err)
	}
	if mfe.Flag != "multicast-secret" {
		t.Fatalf("MissingFlagError.Flag = %q, want %q", mfe.Flag, "multicast-secret")
	}
}

// ---------------------------------------------------------------------------
// buildStaticDiscovery tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// buildDNSDiscovery tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// buildMulticastDiscovery tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// newVersionCmd execution test
// ---------------------------------------------------------------------------

func TestNewVersionCmd_Execute(t *testing.T) {
	// newVersionCmd uses fmt.Printf (writes to os.Stdout), so we
	// capture stdout by temporarily redirecting it through a pipe.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdout = w

	cmd := newVersionCmd()
	cmd.SetArgs([]string{})

	execErr := cmd.Execute()

	// Close the write end and restore stdout before reading.
	_ = w.Close()
	os.Stdout = origStdout

	if execErr != nil {
		t.Fatalf("Execute() error = %v", execErr)
	}

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	_ = r.Close()

	output := buf.String()
	if !strings.Contains(output, "QuicRaft DB v") {
		t.Fatalf("output %q does not contain version header", output)
	}
	if !strings.Contains(output, "commit:") {
		t.Fatalf("output %q does not contain commit info", output)
	}
	if !strings.Contains(output, "built:") {
		t.Fatalf("output %q does not contain build date", output)
	}
}

// ---------------------------------------------------------------------------
// newStatusCmd execution test (with test TLS server)
// ---------------------------------------------------------------------------

func TestNewStatusCmd_Execute(t *testing.T) {
	// Create a test TLS server serving /status JSON.
	statusResp := struct {
		NodeID      string `json:"node_id"`
		RaftAddress string `json:"raft_address"`
		IsLeader    bool   `json:"is_leader"`
		LeaderID    uint64 `json:"leader_id"`
		ShardCount  int    `json:"shard_count"`
		IsDraining  bool   `json:"is_draining"`
		Shards      []struct {
			ShardID     uint64 `json:"shard_id"`
			ReplicaID   uint64 `json:"replica_id"`
			LeaderID    uint64 `json:"leader_id"`
			Term        uint64 `json:"term"`
			IsLeader    bool   `json:"is_leader"`
			Health      string `json:"health"`
			LastApplied uint64 `json:"last_applied"`
		} `json:"shards"`
	}{
		NodeID:      "node-1",
		RaftAddress: "127.0.0.1:63001",
		IsLeader:    true,
		LeaderID:    1,
		ShardCount:  1,
		Shards: []struct {
			ShardID     uint64 `json:"shard_id"`
			ReplicaID   uint64 `json:"replica_id"`
			LeaderID    uint64 `json:"leader_id"`
			Term        uint64 `json:"term"`
			IsLeader    bool   `json:"is_leader"`
			Health      string `json:"health"`
			LastApplied uint64 `json:"last_applied"`
		}{
			{ShardID: 1, ReplicaID: 1, LeaderID: 1, Term: 5, IsLeader: true, Health: "healthy", LastApplied: 100},
		},
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(statusResp)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()

	// Write the test server's CA cert to a temp file for --tls-ca-file.
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Extract host:port from the test server URL (strip "https://").
	addr := strings.TrimPrefix(srv.URL, "https://")

	cmd := newStatusCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--address", addr, "--tls-ca-file", caFile})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "node-1") {
		t.Fatalf("output %q does not contain node ID", output)
	}
	if !strings.Contains(output, "127.0.0.1:63001") {
		t.Fatalf("output %q does not contain raft address", output)
	}
	if !strings.Contains(output, "Shard 1:") {
		t.Fatalf("output %q does not contain shard info", output)
	}
	if !strings.Contains(output, "healthy") {
		t.Fatalf("output %q does not contain health", output)
	}
}

func TestNewStatusCmd_Execute_ServerError(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	addr := strings.TrimPrefix(srv.URL, "https://")

	cmd := newStatusCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--address", addr, "--tls-ca-file", caFile})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil for server error")
	}
}

// ---------------------------------------------------------------------------
// newPeersCmd execution test (with test TLS server)
// ---------------------------------------------------------------------------

func TestNewPeersCmd_Execute(t *testing.T) {
	peersResp := struct {
		Shards []struct {
			ShardID  uint64 `json:"shard_id"`
			LeaderID uint64 `json:"leader_id"`
			Peers    []struct {
				ReplicaID uint64 `json:"replica_id"`
				Address   string `json:"address"`
				IsLeader  bool   `json:"is_leader"`
			} `json:"peers"`
		} `json:"shards"`
	}{
		Shards: []struct {
			ShardID  uint64 `json:"shard_id"`
			LeaderID uint64 `json:"leader_id"`
			Peers    []struct {
				ReplicaID uint64 `json:"replica_id"`
				Address   string `json:"address"`
				IsLeader  bool   `json:"is_leader"`
			} `json:"peers"`
		}{
			{
				ShardID:  1,
				LeaderID: 1,
				Peers: []struct {
					ReplicaID uint64 `json:"replica_id"`
					Address   string `json:"address"`
					IsLeader  bool   `json:"is_leader"`
				}{
					{ReplicaID: 1, Address: "10.0.0.1:63001", IsLeader: true},
					{ReplicaID: 2, Address: "10.0.0.2:63001", IsLeader: false},
				},
			},
		},
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(peersResp)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	addr := strings.TrimPrefix(srv.URL, "https://")

	cmd := newPeersCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--address", addr, "--tls-ca-file", caFile})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Shard 1") {
		t.Fatalf("output %q does not contain shard info", output)
	}
	if !strings.Contains(output, "replica 1") {
		t.Fatalf("output %q does not contain replica 1", output)
	}
	if !strings.Contains(output, "replica 2") {
		t.Fatalf("output %q does not contain replica 2", output)
	}
	if !strings.Contains(output, "10.0.0.1:63001") {
		t.Fatalf("output %q does not contain peer address", output)
	}
	// Leader marker should be present.
	if !strings.Contains(output, "* ") {
		t.Fatalf("output %q does not contain leader marker", output)
	}
}

func TestNewPeersCmd_Execute_ServerError(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	addr := strings.TrimPrefix(srv.URL, "https://")

	cmd := newPeersCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--address", addr, "--tls-ca-file", caFile})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil for server error")
	}
}

// ---------------------------------------------------------------------------
// newStartCmd RunE error paths
// ---------------------------------------------------------------------------

// An unreadable --config aborts the command, and it is the config load that
// aborts it.
//
// The error type is asserted, not just its presence, because this is the only
// test that holds newStartCmd's RunE to resolving the configuration at all.
// Merely requiring a non-nil error does not: a RunE that ignored --config
// entirely and fell through to startup would fail for its own reasons and
// still pass. Removing the resolveStartFlags call was measured to leave the
// weaker form green.
func TestNewStartCmd_InvalidConfig(t *testing.T) {
	const path = "/nonexistent/path/config.yaml"

	cmd := newStartCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--config", path})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil for invalid config path")
	}
	var loadErr *qrdbconfig.LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("error type = %T, want *config.LoadError; err = %v", err, err)
	}
	if loadErr.Path != path {
		t.Errorf("LoadError.Path = %q, want %q", loadErr.Path, path)
	}
}

func TestNewStartCmd_InvalidLogLevel(t *testing.T) {
	cmd := newStartCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"--log-level", "invalid-level",
		"--discovery", "static",
		"--peers", "1=127.0.0.1:63001",
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil for invalid log level")
	}
}

func TestNewStartCmd_InvalidLogFormat(t *testing.T) {
	cmd := newStartCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"--log-format", "xml",
		"--discovery", "static",
		"--peers", "1=127.0.0.1:63001",
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil for invalid log format")
	}
}

// ---------------------------------------------------------------------------
// initializeNode error paths (tested via startShard/resolveCreateFunc)
// ---------------------------------------------------------------------------

func TestStartShard_StaticWithEmptyPeersReturnsNoPeersError(t *testing.T) {
	f := &startFlags{
		discoveryMethod: "static",
		peers:           []string{},
	}
	node := &mockRaftNode{}

	err := startShard(context.Background(), f, node, nil, quicraftconfig.Config{})
	if err == nil {
		t.Fatal("startShard() error = nil, want NoPeersError")
	}
	var npe *NoPeersError
	if !errors.As(err, &npe) {
		t.Fatalf("error type = %T, want *NoPeersError", err)
	}
}

// ---------------------------------------------------------------------------
// Additional edge case tests
// ---------------------------------------------------------------------------

func TestStartShardStatic_JoinFalse(t *testing.T) {
	var receivedJoin bool
	node := &mockRaftNode{
		startShardFn: func(_ map[uint64]string, join bool, _ sm.CreateFunc, _ quicraftconfig.Config) error {
			receivedJoin = join
			return nil
		},
	}

	f := &startFlags{
		peers: []string{"1=127.0.0.1:63001"},
		join:  false,
	}

	err := startShardStatic(f, node, nil, quicraftconfig.Config{})
	if err != nil {
		t.Fatalf("startShardStatic() error = %v, want nil", err)
	}
	if receivedJoin {
		t.Fatal("join = true, want false")
	}
}

func TestStartShardStatic_ShardConfigPassedThrough(t *testing.T) {
	var receivedCfg quicraftconfig.Config
	node := &mockRaftNode{
		startShardFn: func(_ map[uint64]string, _ bool, _ sm.CreateFunc, cfg quicraftconfig.Config) error {
			receivedCfg = cfg
			return nil
		},
	}

	f := &startFlags{
		peers: []string{"1=127.0.0.1:63001"},
	}
	shardCfg := quicraftconfig.Config{ShardID: 5, ReplicaID: 3}

	err := startShardStatic(f, node, nil, shardCfg)
	if err != nil {
		t.Fatalf("startShardStatic() error = %v, want nil", err)
	}
	if receivedCfg.ShardID != 5 {
		t.Fatalf("ShardID = %d, want 5", receivedCfg.ShardID)
	}
	if receivedCfg.ReplicaID != 3 {
		t.Fatalf("ReplicaID = %d, want 3", receivedCfg.ReplicaID)
	}
}

func TestShutdownNode_WithMockRaftNode(t *testing.T) {
	node := &mockRaftNode{closeErr: nil}
	err := shutdownNode(node)
	if err != nil {
		t.Fatalf("shutdownNode() error = %v, want nil", err)
	}
}

func TestShutdownNode_WithMockRaftNodeError(t *testing.T) {
	sentinel := errors.New("close failed")
	node := &mockRaftNode{closeErr: sentinel}
	err := shutdownNode(node)
	if !errors.Is(err, sentinel) {
		t.Fatalf("shutdownNode() error = %v, want %v", err, sentinel)
	}
}

// ---------------------------------------------------------------------------
// newStatusCmd and newPeersCmd connection failure tests
// ---------------------------------------------------------------------------

func TestNewStatusCmd_ConnectionRefused(t *testing.T) {
	// Use a port that is almost certainly not listening.
	cmd := newStatusCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--address", "127.0.0.1:1"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want connection error")
	}
}

func TestNewPeersCmd_ConnectionRefused(t *testing.T) {
	cmd := newPeersCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--address", "127.0.0.1:1"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want connection error")
	}
}

// ---------------------------------------------------------------------------
// newPeersCmd output formatting: multiple shards and sorting
// ---------------------------------------------------------------------------

func TestNewPeersCmd_MultipleShards(t *testing.T) {
	peersResp := struct {
		Shards []struct {
			ShardID  uint64 `json:"shard_id"`
			LeaderID uint64 `json:"leader_id"`
			Peers    []struct {
				ReplicaID uint64 `json:"replica_id"`
				Address   string `json:"address"`
				IsLeader  bool   `json:"is_leader"`
			} `json:"peers"`
		} `json:"shards"`
	}{
		Shards: []struct {
			ShardID  uint64 `json:"shard_id"`
			LeaderID uint64 `json:"leader_id"`
			Peers    []struct {
				ReplicaID uint64 `json:"replica_id"`
				Address   string `json:"address"`
				IsLeader  bool   `json:"is_leader"`
			} `json:"peers"`
		}{
			{
				ShardID:  1,
				LeaderID: 2,
				Peers: []struct {
					ReplicaID uint64 `json:"replica_id"`
					Address   string `json:"address"`
					IsLeader  bool   `json:"is_leader"`
				}{
					{ReplicaID: 3, Address: "10.0.0.3:63001", IsLeader: false},
					{ReplicaID: 1, Address: "10.0.0.1:63001", IsLeader: false},
					{ReplicaID: 2, Address: "10.0.0.2:63001", IsLeader: true},
				},
			},
			{
				ShardID:  2,
				LeaderID: 1,
				Peers: []struct {
					ReplicaID uint64 `json:"replica_id"`
					Address   string `json:"address"`
					IsLeader  bool   `json:"is_leader"`
				}{
					{ReplicaID: 1, Address: "10.0.0.1:63001", IsLeader: true},
				},
			},
		},
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(peersResp)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	addr := strings.TrimPrefix(srv.URL, "https://")

	cmd := newPeersCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--address", addr, "--tls-ca-file", caFile})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Shard 1") {
		t.Fatalf("output missing Shard 1")
	}
	if !strings.Contains(output, "Shard 2") {
		t.Fatalf("output missing Shard 2")
	}

	// Verify sorting: replica 1 should appear before replica 3 in shard 1 output.
	idx1 := strings.Index(output, "replica 1")
	idx3 := strings.Index(output, "replica 3")
	if idx1 < 0 || idx3 < 0 {
		t.Fatalf("output missing expected replicas")
	}
	if idx1 > idx3 {
		t.Fatal("peers not sorted: replica 1 should appear before replica 3")
	}
}

// ---------------------------------------------------------------------------
// newStatusCmd with multiple shards
// ---------------------------------------------------------------------------

func TestNewStatusCmd_MultipleShards(t *testing.T) {
	statusResp := struct {
		NodeID      string `json:"node_id"`
		RaftAddress string `json:"raft_address"`
		IsLeader    bool   `json:"is_leader"`
		LeaderID    uint64 `json:"leader_id"`
		ShardCount  int    `json:"shard_count"`
		IsDraining  bool   `json:"is_draining"`
		Shards      []struct {
			ShardID     uint64 `json:"shard_id"`
			ReplicaID   uint64 `json:"replica_id"`
			LeaderID    uint64 `json:"leader_id"`
			Term        uint64 `json:"term"`
			IsLeader    bool   `json:"is_leader"`
			Health      string `json:"health"`
			LastApplied uint64 `json:"last_applied"`
		} `json:"shards"`
	}{
		NodeID:      "node-42",
		RaftAddress: "10.0.0.1:63001",
		ShardCount:  2,
		Shards: []struct {
			ShardID     uint64 `json:"shard_id"`
			ReplicaID   uint64 `json:"replica_id"`
			LeaderID    uint64 `json:"leader_id"`
			Term        uint64 `json:"term"`
			IsLeader    bool   `json:"is_leader"`
			Health      string `json:"health"`
			LastApplied uint64 `json:"last_applied"`
		}{
			{ShardID: 1, ReplicaID: 1, LeaderID: 1, Term: 10, IsLeader: true, Health: "healthy", LastApplied: 200},
			{ShardID: 2, ReplicaID: 2, LeaderID: 1, Term: 3, IsLeader: false, Health: "no-leader", LastApplied: 50},
		},
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(statusResp)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	addr := strings.TrimPrefix(srv.URL, "https://")

	cmd := newStatusCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--address", addr, "--tls-ca-file", caFile})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "node-42") {
		t.Fatalf("output missing node ID")
	}
	if !strings.Contains(output, "Shard 1:") {
		t.Fatalf("output missing Shard 1")
	}
	if !strings.Contains(output, "Shard 2:") {
		t.Fatalf("output missing Shard 2")
	}
	if !strings.Contains(output, "no-leader") {
		t.Fatalf("output missing health status")
	}
	if !strings.Contains(output, "Shard Count:  2") {
		t.Fatalf("output missing shard count")
	}
}

// ---------------------------------------------------------------------------
// newStartCmd with valid YAML config file
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// buildMulticastDiscovery success path
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// startShardWithBootstrap with static discovery (full happy path)
// ---------------------------------------------------------------------------

func TestStartShardWithBootstrap_StaticDiscovery(t *testing.T) {
	// Use the static discovery builder which does not need network
	// infrastructure, and a mock node that records the call.
	var calledStartShard bool
	node := &mockRaftNode{
		startShardFn: func(_ map[uint64]string, _ bool, _ sm.CreateFunc, _ quicraftconfig.Config) error {
			calledStartShard = true
			return nil
		},
	}

	f := &startFlags{
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001", "2=127.0.0.2:63001"},
		replicaID:       1,
		raftAddress:     "127.0.0.1:63001",
		shardID:         1,
	}

	err := startShardWithBootstrap(context.Background(), f, node, nil, quicraftconfig.Config{ShardID: 1, ReplicaID: 1})
	if err != nil {
		t.Fatalf("startShardWithBootstrap() error = %v, want nil", err)
	}
	if !calledStartShard {
		t.Fatal("node.StartShard was not called")
	}
}

func TestStartShardWithBootstrap_StaticDiscoveryNodeError(t *testing.T) {
	sentinel := errors.New("bootstrap failed")
	node := &mockRaftNode{
		startShardFn: func(_ map[uint64]string, _ bool, _ sm.CreateFunc, _ quicraftconfig.Config) error {
			return sentinel
		},
	}

	f := &startFlags{
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		replicaID:       1,
		raftAddress:     "127.0.0.1:63001",
		shardID:         1,
		host:            testBindHost,
	}

	err := startShardWithBootstrap(context.Background(), f, node, nil, quicraftconfig.Config{ShardID: 1, ReplicaID: 1})
	if err == nil {
		t.Fatal("startShardWithBootstrap() error = nil, want non-nil")
	}
}

// ---------------------------------------------------------------------------
// newStartCmd RunE exercising the full RunE path (log config + resolveAutoConfig + runStart)
// ---------------------------------------------------------------------------

func TestNewStartCmd_UnknownSM_NoConfig(t *testing.T) {
	// This exercises the RunE path without --config:
	// logCfg.SetDefaults -> logCfg.Validate -> resolveAutoConfig -> runStart -> initializeNode
	// It fails at resolveCreateFunc with UnknownSMError.
	//
	// --data-dir is given an empty temporary directory rather than left at its
	// "./data" default: anything the package directory happens to contain
	// would be read as partial TLS material, and clustertls would fail before
	// the state machine name is ever resolved.
	cmd := newStartCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"--sm", "invalid-sm",
		"--discovery", "static",
		"--peers", "1=127.0.0.1:63001",
		"--data-dir", t.TempDir(),
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want UnknownSMError")
	}
	var smErr *UnknownSMError
	if !errors.As(err, &smErr) {
		t.Fatalf("error type = %T, want *UnknownSMError; err = %v", err, err)
	}
}

func TestNewStartCmd_DebugRaftFlag(t *testing.T) {
	// Test --debug-raft flag path. Still fails at runStart, but exercises
	// the debugRaft branch.
	cmd := newStartCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"--debug-raft",
		"--sm", "nonexistent",
		"--discovery", "static",
		"--peers", "1=127.0.0.1:63001",
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
}

func TestNewStartCmd_MemorySM_SingleNodeStatic_NoPeers(t *testing.T) {
	// Test the RunE path with a valid SM type (memory) in single-node
	// static mode with no peers. This exercises initializeNode past
	// resolveCreateFunc into buildHostConfig, isSingleNode (true),
	// quicraft.NewHost (WithoutTransport), and startShardStatic which
	// returns NoPeersError. The host.Close() cleanup path is also covered.
	//
	// --auth-enabled=false: with no --config, auth's own flag default is
	// true, which now defaults the seal subsystem on too (go-qrdb#548) and
	// routes boot through the sealed window -- a real ceremony is a
	// different test's concern (TestRunStart_UnsealCeremony_*). This test is
	// about startShardStatic's NoPeersError, reached only once past that
	// window, so it opts out of it the same way an operator who does not
	// want auth or seal does.
	dir := t.TempDir()
	cmd := newStartCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"--sm", "memory",
		"--discovery", "static",
		"--data-dir", dir,
		"--shard-id", "1",
		"--replica-id", "1",
		"--auth-enabled=false",
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want NoPeersError")
	}
	var npe *NoPeersError
	if !errors.As(err, &npe) {
		t.Fatalf("error type = %T, want *NoPeersError; err = %v", err, err)
	}
}

func TestNewStartCmd_MemorySM_SingleNodeStatic_WithPeer(t *testing.T) {
	// Test the RunE path with a single peer. This exercises initializeNode
	// all the way through to runStart: host creation, startShard success,
	// TLS generation, service creation, and server start. It blocks until
	// the process is signalled, so SIGINT is sent once the node serves to
	// unblock it.
	//
	// --sm memory runs all the way through first boot, whose atomic
	// role/user creation (go-qrdb#555) commits guarded batches: since
	// go-qrdb#136 every state machine serves them natively, and this is the
	// end-to-end proof that a non-default one does.
	//
	// --auth-enabled=false for the same reason as _NoPeers above: with no
	// --config, auth's flag default (true) now defaults the seal subsystem
	// on too (go-qrdb#548), routing boot through the sealed window, which
	// here would wait forever for a ceremony this test never runs. This
	// test is about the full start-to-serving handover with a real peer,
	// which a ceremony is orthogonal to.
	//
	// Every listener this boot opens gets a reserved port. The flag
	// defaults are fixed numbers -- raft 63001, REST 63002, gRPC 63003,
	// Noise 63006 -- so leaving any of them to its default makes this test
	// depend on that port being free on the host, and it went red on
	// "0.0.0.0:63002: bind: address already in use" beside a concurrent
	// integration run. The bind host moves to testBindHost with the ports,
	// and is set on the flags before reserveNoisePort so the Noise port is
	// probed where its listener binds. The ports are drawn per attempt by
	// startDaemon, which starts again on fresh ones if another process takes
	// one before Execute binds it.
	serving := servingHook(t)
	done := startDaemon(t, func(p daemonPorts) daemonLaunch {
		raftAddr := net.JoinHostPort(testBindHost, fmt.Sprint(p.raft))
		cmd, f := newStartCmdWithFlags()
		f.host = testBindHost
		reserveNoisePort(t, f)
		buf := &bytes.Buffer{}
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{
			"--sm", "memory",
			"--discovery", "static",
			"--host", testBindHost,
			"--listen", net.JoinHostPort(raftListenHost, fmt.Sprint(p.raft)),
			"--raft-address", raftAddr,
			"--peers", "1=" + raftAddr,
			"--admin-port", fmt.Sprint(p.admin),
			"--grpc-port", fmt.Sprint(p.grpc),
			"--data-dir", t.TempDir(),
			"--shard-id", "1",
			"--replica-id", "1",
			"--auth-enabled=false",
		})
		return daemonLaunch{run: cmd.Execute, ready: hookFired(serving)}
	})

	// Signal once the server is serving, and keep signalling until
	// Execute returns: see signalAfterReady (go-qrdb#83).
	if err := signalUntilReturn(t, done, syscall.SIGINT); err != nil {
		t.Fatalf("Execute() error = %v, want nil (clean shutdown)", err)
	}
	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// A file-supplied configuration is overridden by the CLI flag for the same
// setting.
//
// What this exercises is config loading + applyConfigToFlags, so it asserts on
// those and not on the startup error that used to stand in for them: reaching
// UnknownSMError meant running most of runStart first, and any of that work
// can fail earlier under load (go-qrdb#84).
func TestNewStartCmd_ValidConfig_SMUnknown(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Use a valid config but override sm to "unknown" via CLI flag.
	yamlContent := `
server:
  admin_port: 19443
  grpc_port: 19444
  host: "0.0.0.0"
protocols:
  rest: true
  grpc: false
raft:
  data_dir: "` + filepath.Join(dir, "data") + `"
  deployment_id: 1
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:19001"
  peers: "1=127.0.0.1:19001"
tls:
  enabled: false
  client_auth: "require_and_verify"
  min_version: "TLS1.3"
logging:
  level: "info"
  format: "text"
discovery:
  method: "static"
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cmd, f := newStartCmdWithFlags()
	// The YAML has no sm_type, so without the flag the default "pebble"
	// would apply.
	if err := cmd.ParseFlags([]string{"--config", cfgPath, "--sm", "unknown-sm"}); err != nil {
		t.Fatalf("ParseFlags() error = %v", err)
	}
	if err := resolveStartFlags(cmd, f); err != nil {
		t.Fatalf("resolveStartFlags() error = %v", err)
	}

	if f.smType != "unknown-sm" {
		t.Errorf("smType = %q, want %q: the CLI flag did not override the file", f.smType, "unknown-sm")
	}
	if f.adminPort != 19443 {
		t.Errorf("adminPort = %d, want 19443", f.adminPort)
	}
	if f.grpcPort != 19444 {
		t.Errorf("grpcPort = %d, want 19444", f.grpcPort)
	}
	if f.raftAddress != "127.0.0.1:19001" {
		t.Errorf("raftAddress = %q, want %q", f.raftAddress, "127.0.0.1:19001")
	}
	if f.dataDir != filepath.Join(dir, "data") {
		t.Errorf("dataDir = %q, want %q", f.dataDir, filepath.Join(dir, "data"))
	}
	if !f.enableREST || f.enableGRPC {
		t.Errorf("protocols = rest:%v grpc:%v, want rest:true grpc:false", f.enableREST, f.enableGRPC)
	}
}

// ---------------------------------------------------------------------------
// loadTLSFromFiles tests
// ---------------------------------------------------------------------------

func TestLoadTLSFromFiles_Valid(t *testing.T) {
	// Generate a real bundle then write its PEM components to temp files.
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS() error: %v", err)
	}

	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	if err := os.WriteFile(caPath, bundle.CACertPEM, 0o600); err != nil {
		t.Fatalf("WriteFile(ca) error: %v", err)
	}
	if err := os.WriteFile(certPath, bundle.CertPEM, 0o600); err != nil {
		t.Fatalf("WriteFile(cert) error: %v", err)
	}
	if err := os.WriteFile(keyPath, bundle.KeyPEM, 0o600); err != nil {
		t.Fatalf("WriteFile(key) error: %v", err)
	}

	loaded, err := loadTLSFromFiles(caPath, certPath, keyPath)
	if err != nil {
		t.Fatalf("loadTLSFromFiles() error: %v", err)
	}
	if loaded == nil {
		t.Fatal("loadTLSFromFiles() returned nil bundle")
	}
	if loaded.TLSConfig == nil {
		t.Fatal("TLSConfig is nil")
	}
	if loaded.CACertPool == nil {
		t.Fatal("CACertPool is nil")
	}
	if len(loaded.CACertPEM) == 0 {
		t.Fatal("CACertPEM is empty")
	}
	if len(loaded.CertPEM) == 0 {
		t.Fatal("CertPEM is empty")
	}
	if len(loaded.KeyPEM) == 0 {
		t.Fatal("KeyPEM is empty")
	}
}

func TestLoadTLSFromFiles_InvalidCAPath(t *testing.T) {
	_, err := loadTLSFromFiles("/nonexistent/ca.pem", "/nonexistent/cert.pem", "/nonexistent/key.pem")
	if err == nil {
		t.Fatal("loadTLSFromFiles() error = nil, want TLSLoadError")
	}
	var tlsErr *TLSLoadError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("error type = %T, want *TLSLoadError", err)
	}
	if tlsErr.Path != "/nonexistent/ca.pem" {
		t.Fatalf("TLSLoadError.Path = %q, want %q", tlsErr.Path, "/nonexistent/ca.pem")
	}
}

func TestLoadTLSFromFiles_InvalidCertPath(t *testing.T) {
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS() error: %v", err)
	}

	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, bundle.CACertPEM, 0o600); err != nil {
		t.Fatalf("WriteFile(ca) error: %v", err)
	}

	_, err = loadTLSFromFiles(caPath, "/nonexistent/cert.pem", "/nonexistent/key.pem")
	if err == nil {
		t.Fatal("loadTLSFromFiles() error = nil, want TLSLoadError")
	}
	var tlsErr *TLSLoadError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("error type = %T, want *TLSLoadError", err)
	}
	if tlsErr.Path != "/nonexistent/cert.pem" {
		t.Fatalf("TLSLoadError.Path = %q, want %q", tlsErr.Path, "/nonexistent/cert.pem")
	}
}

func TestLoadTLSFromFiles_InvalidKeyPath(t *testing.T) {
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS() error: %v", err)
	}

	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	certPath := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(caPath, bundle.CACertPEM, 0o600); err != nil {
		t.Fatalf("WriteFile(ca) error: %v", err)
	}
	if err := os.WriteFile(certPath, bundle.CertPEM, 0o600); err != nil {
		t.Fatalf("WriteFile(cert) error: %v", err)
	}

	_, err = loadTLSFromFiles(caPath, certPath, "/nonexistent/key.pem")
	if err == nil {
		t.Fatal("loadTLSFromFiles() error = nil, want TLSLoadError")
	}
	var tlsErr *TLSLoadError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("error type = %T, want *TLSLoadError", err)
	}
	if tlsErr.Path != "/nonexistent/key.pem" {
		t.Fatalf("TLSLoadError.Path = %q, want %q", tlsErr.Path, "/nonexistent/key.pem")
	}
}

// ---------------------------------------------------------------------------
// loadCACertPool tests
// ---------------------------------------------------------------------------

func TestLoadCACertPool_EmptyPath(t *testing.T) {
	pool, err := loadCACertPool("")
	if err != nil {
		t.Fatalf("loadCACertPool(\"\") error: %v", err)
	}
	if pool != nil {
		t.Fatal("loadCACertPool(\"\") returned non-nil pool, want nil")
	}
}

func TestLoadCACertPool_ValidPath(t *testing.T) {
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS() error: %v", err)
	}

	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, bundle.CACertPEM, 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	pool, err := loadCACertPool(caPath)
	if err != nil {
		t.Fatalf("loadCACertPool() error: %v", err)
	}
	if pool == nil {
		t.Fatal("loadCACertPool() returned nil pool, want non-nil")
	}
}

func TestLoadCACertPool_InvalidPath(t *testing.T) {
	_, err := loadCACertPool("/nonexistent/ca.pem")
	if err == nil {
		t.Fatal("loadCACertPool() error = nil, want TLSLoadError")
	}
	var tlsErr *TLSLoadError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("error type = %T, want *TLSLoadError", err)
	}
	if tlsErr.Path != "/nonexistent/ca.pem" {
		t.Fatalf("TLSLoadError.Path = %q, want %q", tlsErr.Path, "/nonexistent/ca.pem")
	}
}

func TestLoadCACertPool_InvalidPEM(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(caPath, []byte("not-a-pem-file"), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	_, err := loadCACertPool(caPath)
	if err == nil {
		t.Fatal("loadCACertPool() error = nil, want TLSLoadError")
	}
	var tlsErr *TLSLoadError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("error type = %T, want *TLSLoadError", err)
	}
}

// ---------------------------------------------------------------------------
// Bootstrap & reload error type tests
// ---------------------------------------------------------------------------

func TestBootstrapFetchError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("connection reset")
	err := &BootstrapFetchError{Method: "HTTPS", Err: inner}
	want := "clusterenroll: bootstrap fetch failed via HTTPS: connection reset"
	if err.Error() != want {
		t.Fatalf("BootstrapFetchError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("BootstrapFetchError.Unwrap() did not return inner error")
	}
}

func TestBootstrapFetchError_ErrorsIs(t *testing.T) {
	inner := errors.New("timeout")
	err := &BootstrapFetchError{Method: "mDNS", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is() should find inner error through Unwrap")
	}
}

func TestBootstrapServerError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("address in use")
	err := &BootstrapServerError{Protocol: "QUIC", Err: inner}
	want := "clusterenroll: bootstrap server (QUIC) failed to start: address in use"
	if err.Error() != want {
		t.Fatalf("BootstrapServerError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("BootstrapServerError.Unwrap() did not return inner error")
	}
}

func TestBootstrapServerError_ErrorsIs(t *testing.T) {
	inner := errors.New("bind failed")
	err := &BootstrapServerError{Protocol: "TCP", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is() should find inner error through Unwrap")
	}
}

func TestBootstrapWriteError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("disk full")
	err := &BootstrapWriteError{Path: "/var/qrdb/bootstrap.pem", Err: inner}
	want := `clustertls: failed to write file "/var/qrdb/bootstrap.pem": disk full`
	if err.Error() != want {
		t.Fatalf("BootstrapWriteError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("BootstrapWriteError.Unwrap() did not return inner error")
	}
}

func TestBootstrapWriteError_ErrorsIs(t *testing.T) {
	inner := errors.New("permission denied")
	err := &BootstrapWriteError{Path: "/etc/ca.pem", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is() should find inner error through Unwrap")
	}
}

func TestBootstrapKeyError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("invalid PEM block")
	err := &BootstrapKeyError{Kind: "ED25519", Err: inner}
	want := "clustertls: key parse failed (ED25519): invalid PEM block"
	if err.Error() != want {
		t.Fatalf("BootstrapKeyError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("BootstrapKeyError.Unwrap() did not return inner error")
	}
}

func TestBootstrapKeyError_ErrorsIs(t *testing.T) {
	inner := errors.New("unsupported curve")
	err := &BootstrapKeyError{Kind: "ECDSA", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is() should find inner error through Unwrap")
	}
}

func TestCertReloadError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("file not found")
	err := &CertReloadError{Path: "/etc/certs/node.pem", Err: inner}
	want := `cli: TLS certificate reload failed for "/etc/certs/node.pem": file not found`
	if err.Error() != want {
		t.Fatalf("CertReloadError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("CertReloadError.Unwrap() did not return inner error")
	}
}

func TestCertReloadError_ErrorsIs(t *testing.T) {
	inner := errors.New("bad certificate")
	err := &CertReloadError{Path: "/data/tls.pem", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is() should find inner error through Unwrap")
	}
}

// ---------------------------------------------------------------------------
// generateCA tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// generateNodeCert tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// loadTLSFromDir tests
// ---------------------------------------------------------------------------

func TestLoadTLSFromDir_Present(t *testing.T) {
	// Generate a bundle and persist it to a temp directory.
	bundle, err := generateCATLS()
	require.NoError(t, err)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.pem"), bundle.CACertPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca-key.pem"), bundle.CAKeyPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "node-cert.pem"), bundle.CertPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "node-key.pem"), bundle.KeyPEM, 0o600))

	loaded, err := loadTLSFromDir(dir)
	require.NoError(t, err)
	require.NotNil(t, loaded, "loadTLSFromDir should return a bundle when files exist")

	assert.NotEmpty(t, loaded.CACertPEM, "CACertPEM should be populated")
	assert.NotEmpty(t, loaded.CAKeyPEM, "CAKeyPEM should be populated")
	assert.NotEmpty(t, loaded.CertPEM, "CertPEM should be populated")
	assert.NotEmpty(t, loaded.KeyPEM, "KeyPEM should be populated")
	assert.NotNil(t, loaded.TLSConfig, "TLSConfig should be non-nil")
	assert.NotNil(t, loaded.CACertPool, "CACertPool should be non-nil")
}

func TestLoadTLSFromDir_Absent(t *testing.T) {
	dir := t.TempDir()

	loaded, err := loadTLSFromDir(dir)
	assert.NoError(t, err, "empty dir should not return an error")
	assert.Nil(t, loaded, "empty dir should return nil bundle")
}

func TestLoadTLSFromDir_Partial(t *testing.T) {
	// Only ca.pem exists; node-cert.pem and node-key.pem are missing. A partial
	// directory is corruption, so loadTLSFromDir must fail closed rather than
	// silently regenerating a fresh CA.
	dir := t.TempDir()
	bundle, err := generateCATLS()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.pem"), bundle.CACertPEM, 0o600))

	loaded, err := loadTLSFromDir(dir)
	assert.Nil(t, loaded, "partial files should return nil bundle")
	var partErr *clustertls.PartialMaterialError
	assert.ErrorAs(t, err, &partErr, "partial files should fail closed with PartialMaterialError")
}

// ---------------------------------------------------------------------------
// wireCertReloader tests
// ---------------------------------------------------------------------------

func TestWireCertReloader_WiresCallback(t *testing.T) {
	// Generate and persist a bundle so we have real cert/key files.
	bundle, err := generateCATLS()
	require.NoError(t, err)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "node-cert.pem")
	keyPath := filepath.Join(dir, "node-key.pem")
	require.NoError(t, os.WriteFile(certPath, bundle.CertPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, bundle.KeyPEM, 0o600))

	logger := slog.Default()

	err = wireCertReloader(bundle, certPath, keyPath, logger)
	require.NoError(t, err)

	// Verify GetCertificate is wired and Certificates is cleared.
	assert.NotNil(t, bundle.TLSConfig.GetCertificate,
		"GetCertificate should be non-nil after wiring")
	assert.Nil(t, bundle.TLSConfig.Certificates,
		"Certificates should be nil after wiring (GetCertificate takes precedence)")
	assert.NotNil(t, bundle.Reloader,
		"Reloader should be set on the bundle")

	// Clean up the reloader goroutine.
	require.NoError(t, bundle.Reloader.Stop())
}

// ---------------------------------------------------------------------------
// Helper: makePEMBlock
// ---------------------------------------------------------------------------

func makePEMBlock(blockType string, data []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: data})
}

// ---------------------------------------------------------------------------
// Phase 6: loadTLSFromDir additional paths
// ---------------------------------------------------------------------------

func TestLoadTLSFromDir_NoCAKey(t *testing.T) {
	dir := t.TempDir()
	bundle := writeTLSFixtures(t, dir)

	// Remove ca-key.pem so loadTLSFromDir skips it.
	require.NoError(t, os.Remove(filepath.Join(dir, "ca-key.pem")))

	loaded, err := loadTLSFromDir(dir)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	// Core cert material should be loaded.
	assert.Equal(t, bundle.CACertPEM, loaded.CACertPEM)
	assert.Equal(t, bundle.CertPEM, loaded.CertPEM)
	assert.Equal(t, bundle.KeyPEM, loaded.KeyPEM)

	// CAKeyPEM should be empty since ca-key.pem was removed.
	assert.Empty(t, loaded.CAKeyPEM, "CAKeyPEM should be empty when ca-key.pem is absent")
}

func TestLoadTLSFromDir_CAKeyReadError(t *testing.T) {
	dir := t.TempDir()
	writeTLSFixtures(t, dir)

	// Replace ca-key.pem with a symlink to /proc/self/mem so that
	// fileExists returns true (Stat succeeds, not a directory) but
	// os.ReadFile fails with an I/O error. This works even as root,
	// unlike chmod-based permission restrictions.
	caKeyPath := filepath.Join(dir, "ca-key.pem")
	require.NoError(t, os.Remove(caKeyPath))
	require.NoError(t, os.Symlink("/proc/self/mem", caKeyPath))

	_, err := loadTLSFromDir(dir)
	require.Error(t, err)

	var loadErr *TLSLoadError
	assert.True(t, errors.As(err, &loadErr), "error should be *TLSLoadError, got %T", err)
}

// ---------------------------------------------------------------------------
// handleSIGHUP tests
// ---------------------------------------------------------------------------

func TestHandleSIGHUP_NilReloader(t *testing.T) {
	// A nil reloader must be silently ignored without panicking.
	assert.NotPanics(t, func() {
		handleSIGHUP(nil, testLogger())
	})
}

func TestHandleSIGHUP_ReloadSuccess(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, certPEM, keyPEM)

	reloader, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = reloader.Stop() })

	// handleSIGHUP must not panic and the cert must remain valid.
	assert.NotPanics(t, func() {
		handleSIGHUP(reloader, testLogger())
	})

	// Verify the certificate is still loadable after reload.
	cert, err := reloader.GetCertificate(nil)
	require.NoError(t, err)
	require.NotNil(t, cert, "certificate must remain valid after successful SIGHUP reload")
}

func TestHandleSIGHUP_ReloadError(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, certPEM, keyPEM)

	reloader, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = reloader.Stop() })

	// Delete the cert file so reload will fail.
	require.NoError(t, os.Remove(certPath))

	// handleSIGHUP must not panic even when reload fails (error is logged).
	assert.NotPanics(t, func() {
		handleSIGHUP(reloader, testLogger())
	})
}

// ---------------------------------------------------------------------------
// Phase 8: Additional Error Path Tests
// ---------------------------------------------------------------------------

func TestWireCertReloader_InvalidCertPath(t *testing.T) {
	bundle := &TLSBundle{
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS13,
		},
	}

	dir := t.TempDir()
	certPath := filepath.Join(dir, "nonexistent.crt")
	keyPath := filepath.Join(dir, "nonexistent.key")

	err := wireCertReloader(bundle, certPath, keyPath, testLogger())
	require.Error(t, err)

	var reloadErr *CertReloadError
	assert.True(t, errors.As(err, &reloadErr),
		"wireCertReloader with nonexistent paths must return *CertReloadError, got %T", err)
}

func TestLoadTLSFromFiles_MismatchedKeyPair(t *testing.T) {
	dir := t.TempDir()

	// Generate two independent cert/key pairs.
	certA, _ := testCertPair(t)
	_, keyB := testCertPair(t)

	// Generate a valid CA for the CA file.
	caCert, _ := testCertPair(t)

	caFile := filepath.Join(dir, "ca.pem")
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")

	require.NoError(t, os.WriteFile(caFile, caCert, 0o600))
	require.NoError(t, os.WriteFile(certFile, certA, 0o600))
	require.NoError(t, os.WriteFile(keyFile, keyB, 0o600))

	_, err := loadTLSFromFiles(caFile, certFile, keyFile)
	require.Error(t, err)

	var loadErr *TLSLoadError
	assert.True(t, errors.As(err, &loadErr),
		"loadTLSFromFiles with mismatched key pair must return *TLSLoadError, got %T", err)
}

func TestLoadTLSFromFiles_InvalidCAPEM(t *testing.T) {
	dir := t.TempDir()

	certPEM, keyPEM := testCertPair(t)

	caFile := filepath.Join(dir, "ca.pem")
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")

	// Write garbage data as the CA file.
	require.NoError(t, os.WriteFile(caFile, []byte("not-a-valid-pem"), 0o600))
	require.NoError(t, os.WriteFile(certFile, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyFile, keyPEM, 0o600))

	_, err := loadTLSFromFiles(caFile, certFile, keyFile)
	require.Error(t, err)

	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr),
		"loadTLSFromFiles with invalid CA PEM must return *TLSLoadError, got %T", err)
	assert.True(t, errors.Is(loadErr, errNoCertsInPEM),
		"underlying error must be errNoCertsInPEM")
}

// ---------------------------------------------------------------------------
// startBootstrapServers: advertiser failure propagates to the caller
// ---------------------------------------------------------------------------
