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

package lifecycle_test

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestBuildServices_ReplicatedAuditAlwaysWired asserts every BuildServices
// composition -- even the minimal, seal-disabled embedded-GUI shape -- gets
// a non-nil ReplicatedAudit sink: it degrades gracefully (not entitled)
// before a Tenant servicer exists rather than being omitted outright.
func TestBuildServices_ReplicatedAuditAlwaysWired(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.ReplicatedAudit == nil {
		t.Fatal("ReplicatedAudit is nil")
	}

	// With no Tenant servicer wired (seal disabled), every tenant degrades
	// to "not entitled": Query answers Total: -1, never an error.
	page, err := node.ReplicatedAudit.Query(context.Background(), audit.Filter{TenantID: "acme"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if page.Total != -1 {
		t.Fatalf("Total = %d, want -1 (not entitled)", page.Total)
	}
}
