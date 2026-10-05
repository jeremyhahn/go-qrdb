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

//go:build !integration

package node

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newMemoryNodeConfig returns a DefaultConfig wired for an ephemeral
// in-memory backend with all protocol servers disabled so Start() does not
// bind any ports. A temporary directory is provided as DataDir for subsystems
// (e.g. vector storage) that require a disk path even when the primary storage
// backend is in-memory.
func newMemoryNodeConfig(t *testing.T) *qrdbconfig.Config {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	return cfg
}

// TestNodeAuditClient_EmitAndQueryRoundTrip verifies that the AuditLogger
// injected via server.WithAuditLogger is shared with the in-process embedded
// transport: an event emitted through client.Audit().EmitAudit is retrievable
// through client.Audit().QueryAudit against the same node's logger.
func TestNodeAuditClient_EmitAndQueryRoundTrip(t *testing.T) {
	cfg := newMemoryNodeConfig(t)

	// A queryable in-memory logger keeps the test fast and host-clean.
	logger := audit.NewRingBuffer(64)
	serverOpts := []server.ServerOption{server.WithAuditLogger(logger)}

	n, err := NewStandaloneNode(context.Background(), cfg, serverOpts)
	require.NoError(t, err)
	require.NotNil(t, n)
	t.Cleanup(func() { _ = n.Close() })

	client := n.Client()
	require.NotNil(t, client, "Client must be non-nil after successful construction")

	auditClient := client.Audit()
	require.NotNil(t, auditClient, "Audit() must be non-nil when an AuditLogger is injected")

	ctx := context.Background()
	ev := transport.AuditEvent{
		Actor:    "x",
		Action:   "test",
		Resource: "r",
		Outcome:  "allow",
		Attributes: map[string]any{
			"key": "value",
		},
	}
	require.NoError(t, auditClient.EmitAudit(ctx, ev))

	// Filtered on this test's own action: the audit trail is opened before
	// the seal subsystem during boot (go-qrdb#548) and already carries that
	// barrier-initialize record by the time the node is up, so an
	// unfiltered query sees it too.
	page, err := auditClient.QueryAudit(ctx, transport.AuditFilter{Action: "test"})
	require.NoError(t, err)
	require.NotNil(t, page)
	require.Len(t, page.Events, 1, "the emitted event must be retrievable")

	got := page.Events[0]
	assert.Equal(t, "x", got.Actor)
	assert.Equal(t, "test", got.Action)
	assert.Equal(t, "r", got.Resource)
	assert.Equal(t, "allow", got.Outcome)
	assert.Equal(t, "value", got.Attributes["key"])
	assert.False(t, got.Timestamp.IsZero(), "logger must stamp a timestamp")
}

// TestNodeAuditClient_NilWithoutLogger verifies that client.Audit() degrades
// to nil when the node was built without an AuditLogger, so callers can fall
// back rather than receiving a logger-less client.
func TestNodeAuditClient_NilWithoutLogger(t *testing.T) {
	cfg := newMemoryNodeConfig(t)

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, n)
	t.Cleanup(func() { _ = n.Close() })

	client := n.Client()
	require.NotNil(t, client)

	assert.Nil(t, client.Audit(), "Audit() must be nil when no AuditLogger is injected")
}
