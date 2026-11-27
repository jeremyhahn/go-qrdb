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

//go:build qrdb_vector

package service

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestNodeKeyLogProbe_HomeBackedVectorsAreNotNodeKeyed: the probe reads the
// node-local vector store, so an isolated tenant whose vectors live in its
// tenant-keyed home is not reported (its tightening may proceed), while a
// tenant whose vectors are node-local still is.
func TestNodeKeyLogProbe_HomeBackedVectorsAreNotNodeKeyed(t *testing.T) {
	homes := newFakeVectorHomes()
	svc, nodeWide := newHomedVectorService(t, homes)
	for _, id := range []string{"acme", "bt"} {
		ctx := tenant.WithTenantID(context.Background(), id)
		ns := tenant.ComposeNamespace(id, tenant.DefaultDatabase) + "docs"
		if _, err := svc.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: ns, Dimension: 2}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if _, err := svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: ns, ID: 1, Vector: []float32{1, 0}}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}
	if len(vectorHomeKeys(t, homes.home("acme"))) == 0 {
		t.Fatal("acme's vectors must be in its home")
	}

	reader := NewKVSharedNamespaceReader(sharedShardKeys{}, WithSharedVectorNamespaces(nodeWide))
	probe := NewNodeKeyLogProbe(unopenableCatalog{}, WithSharedNamespaces(reader, tenant.DefaultDatabase))

	home, err := probe.NodeKeyLogDatabases(context.Background(), "acme")
	if err != nil {
		t.Fatalf("NodeKeyLogDatabases acme: %v", err)
	}
	if len(home) != 0 {
		t.Fatalf("home-backed tenant: got %v, want none", home)
	}
	local, err := probe.NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil {
		t.Fatalf("NodeKeyLogDatabases bt: %v", err)
	}
	if len(local) != 1 || local[0] != tenant.DefaultDatabase {
		t.Fatalf("node-local tenant: got %v, want [%s]", local, tenant.DefaultDatabase)
	}
}
