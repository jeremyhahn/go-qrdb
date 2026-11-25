//go:build qrdb_embedded && !integration

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestInitEmbeddedBarrier_AuditsBootMigration: the credential core the
// desktop embedded boot builds records on the boot's audit trail, so the
// legacy migration BindStore runs once the replicated store exists is
// audited like the daemon's.
func TestInitEmbeddedBarrier_AuditsBootMigration(t *testing.T) {
	ctx := context.Background()
	trail := &mockAuditLogger{}
	b, _, cred, _, err := initEmbeddedBarrier(t.TempDir(), nil, trail)
	if err != nil {
		t.Fatalf("initEmbeddedBarrier: %v", err)
	}
	// A pre-upgrade copy in the barrier's legacy layout.
	if err := b.Put(ctx, "credentials/db", []byte("legacy")); err != nil {
		t.Fatalf("write legacy credential: %v", err)
	}
	trail.emitted = nil

	if err := cred.BindStore(ctx, seal.NewMemoryBackend()); err != nil {
		t.Fatalf("BindStore: %v", err)
	}
	for _, ev := range trail.emitted {
		if ev.Action == "migrate" && ev.Resource == "credential:db" {
			return
		}
	}
	t.Fatalf("boot migration of credential:db not audited; trail got %+v", trail.emitted)
}
