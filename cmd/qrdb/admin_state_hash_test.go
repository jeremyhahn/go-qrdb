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
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// stateHashAdminService is an admin double implementing
// service.ReplicaStateHasher, recording the shard IDs it was asked for.
type stateHashAdminService struct {
	mockAdminService
	called    bool
	gotShards []uint64
	err       error
}

var _ service.ReplicaStateHasher = (*stateHashAdminService)(nil)

func (m *stateHashAdminService) StateHashes(_ context.Context, shardIDs []uint64) (*service.StateHashesResponse, error) {
	m.called = true
	m.gotShards = shardIDs
	if m.err != nil {
		return nil, m.err
	}
	return &service.StateHashesResponse{
		NodeID: "node-a",
		Replicas: []service.ReplicaStateHash{
			{ShardID: 1, ReplicaID: 2, AppliedIndex: 41, Records: 7, Hash: strings.Repeat("ab", 32)},
			{TenantID: "acme", ShardID: 9, ReplicaID: 2, Error: "state digest unsupported"},
		},
	}, nil
}

// withOutputFormat runs fn with outputFormat set to format.
func withOutputFormat(t *testing.T, format string, fn func()) {
	t.Helper()
	old := outputFormat
	outputFormat = format
	defer func() { outputFormat = old }()
	fn()
}

func TestAdminStateHash_Table(t *testing.T) {
	svc := &stateHashAdminService{}
	out, err := executeAdminCmd(t, svc, "state-hash", "--shard", "1", "--shard", "9")
	if err != nil {
		t.Fatalf("state-hash: %v", err)
	}
	if !slices.Equal(svc.gotShards, []uint64{1, 9}) {
		t.Fatalf("service asked for %v, want [1 9]", svc.gotShards)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("output = %q, want a header and two rows", out)
	}
	if got := strings.Fields(lines[0]); !slices.Equal(got, []string{"TENANT", "SHARD_ID", "REPLICA_ID", "APPLIED_INDEX", "RECORDS", "HASH", "ERROR"}) {
		t.Fatalf("header = %q", lines[0])
	}
	if got := strings.Fields(lines[1]); !slices.Equal(got, []string{"-", "1", "2", "41", "7", strings.Repeat("ab", 32), "-"}) {
		t.Fatalf("system row = %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "acme") || !strings.Contains(lines[2], "state digest unsupported") || strings.Fields(lines[2])[5] != "-" {
		t.Fatalf("tenant row = %q, want acme with its error and no hash", lines[2])
	}

	if _, err := executeAdminCmd(t, svc, "state-hash"); err != nil || svc.gotShards != nil {
		t.Fatalf("state-hash with no --shard = %v (asked %v); want every shard", err, svc.gotShards)
	}
}

func TestAdminStateHash_JSONAndYAML(t *testing.T) {
	svc := &stateHashAdminService{}
	var out string
	var err error
	withOutputFormat(t, "json", func() { out, err = executeAdminCmd(t, svc, "state-hash") })
	if err != nil {
		t.Fatalf("state-hash (json): %v", err)
	}
	var shown transport.StateHashesResponse
	if err := json.Unmarshal([]byte(out), &shown); err != nil || shown.NodeID != "node-a" || len(shown.Replicas) != 2 || shown.Replicas[0].AppliedIndex != 41 {
		t.Fatalf("state-hash json = %q (%v)", out, err)
	}
	if !strings.Contains(out, `"applied_index": 41`) {
		t.Fatalf("state-hash json = %q, want the documented snake_case keys", out)
	}

	withOutputFormat(t, "yaml", func() { out, err = executeAdminCmd(t, svc, "state-hash") })
	if err != nil || !strings.Contains(out, "node_id: node-a") || !strings.Contains(out, "applied_index: 41") {
		t.Fatalf("state-hash yaml = %q, %v; want the documented snake_case keys", out, err)
	}
}

func TestAdminStateHash_Refusals(t *testing.T) {
	svc := &stateHashAdminService{}
	_, err := executeAdminCmd(t, svc, "state-hash", "--shard", "1", "--shard", "-3")
	var invalid *InvalidFlagValueError
	if !errors.As(err, &invalid) || invalid.Flag != "shard" || invalid.Value != "-3" {
		t.Fatalf("a negative --shard = %v, want an InvalidFlagValueError on shard", err)
	}
	if svc.called {
		t.Fatal("a refused --shard reached the service")
	}

	failing := &stateHashAdminService{err: &service.StateDigestUnsupportedError{}}
	if _, err := executeAdminCmd(t, failing, "state-hash"); err == nil {
		t.Fatal("a failing service call must fail the command")
	}

	var unsupported *transport.CapabilityUnsupportedError
	if _, err := executeAdminCmd(t, &mockAdminService{}, "state-hash"); !errors.As(err, &unsupported) {
		t.Fatalf("an admin service without the capability = %v, want *transport.CapabilityUnsupportedError", err)
	}
}

func TestParseShardFlags(t *testing.T) {
	if ids, err := parseShardFlags(nil); ids != nil || err != nil {
		t.Fatalf("no values = (%v, %v), want (nil, nil)", ids, err)
	}
	if ids, err := parseShardFlags([]string{"3", "18446744073709551615"}); err != nil || !slices.Equal(ids, []uint64{3, 1<<64 - 1}) {
		t.Fatalf("(%v, %v)", ids, err)
	}
	if _, err := parseShardFlags([]string{"x"}); err == nil {
		t.Fatal("a non-numeric shard was accepted")
	}
}

// TestStateHashUnsupportedError pins the typed error's message: every SDK
// transport implements transport.ReplicaStateHasher, so the branch is
// unreachable through executeAdminCmd and is pinned directly, matching
// TestObserverMemberAddUnsupportedError.
func TestStateHashUnsupportedError(t *testing.T) {
	if err := (&StateHashUnsupportedError{}); !strings.Contains(err.Error(), "does not support replica state hashes") {
		t.Fatalf("Error() = %q", err.Error())
	}
}

func TestOrDash(t *testing.T) {
	if orDash("") != "-" || orDash("x") != "x" {
		t.Fatalf("orDash = %q, %q", orDash(""), orDash("x"))
	}
}
