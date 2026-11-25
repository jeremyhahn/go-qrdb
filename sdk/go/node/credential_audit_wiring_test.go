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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
)

// TestCredentialCore_RecordsOnTheNodesTrail pins Codex round 1 on #1548: the
// SDK node builds its credential core with the compliance trail, so the boot
// migration and a scheduled rotation record credential audit events on it.
func TestCredentialCore_RecordsOnTheNodesTrail(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// First boot: leave a credential in the node-local (legacy) layout in
	// the barrier, as a node from before the replicated store did.
	first, err := NewStandaloneNode(ctx, sealedStandaloneConfig(t, dir), nil,
		WithSealPassphrase([]byte(nodeSealPass)))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.barrier.Load().Put(ctx, "credentials/db-pass", []byte("legacy-value")); err != nil {
		_ = first.Close()
		t.Fatalf("seed legacy credential: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// Second boot with a recording trail: binding runs the migration.
	trail := audit.NewRingBuffer(256)
	src := func(_ context.Context, _, _ string) (*secure.Buffer, error) {
		return secure.NewBufferFromBytes([]byte("rotated-value"))
	}
	n, err := NewStandaloneNode(ctx, sealedStandaloneConfig(t, dir),
		[]server.ServerOption{server.WithAuditLogger(trail)},
		WithSealPassphrase([]byte(nodeSealPass)), WithCredentialRotationValueSource(src))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })

	svc := n.Services().Credential
	policy := credentials.RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 1}
	if err := svc.CredentialSetRotationPolicy(ctx, "db-pass", policy); err != nil {
		t.Fatalf("CredentialSetRotationPolicy: %v", err)
	}
	rotator, ok := svc.(credentialRotator)
	if !ok {
		t.Fatalf("node credential service %T has no CredentialRotateDue", svc)
	}
	if _, err := rotator.CredentialRotateDue(ctx, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatalf("CredentialRotateDue: %v", err)
	}

	seen := map[string]bool{}
	for _, ev := range trail.Snapshot() {
		seen[ev.Action] = true
	}
	if !seen["migrate"] || !seen["rotate"] {
		t.Fatalf("trail actions = %v, want the boot migration and the scheduled rotation", seen)
	}
}
