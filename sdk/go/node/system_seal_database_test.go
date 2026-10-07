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

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestStandaloneNode_SystemSealMarksBarrierDatabasesSealed: a node seal seals
// every database barrier with the node's, and marks each such database
// sealed -- the mark is what refuses a database whose store does not refuse
// by itself. The database stays sealed across the node unseal: its barrier is
// its owner's, unsealed with the owner's secret, and the owner's unseal opens
// it again.
func TestStandaloneNode_SystemSealMarksBarrierDatabasesSealed(t *testing.T) {
	const passphrase = "whole-node-passphrase"
	const dbSecret = "db-pass-123456"
	ctx := context.Background()
	cfg := sealedStandaloneConfig(t, t.TempDir())
	n, err := NewStandaloneNode(ctx, cfg, nil, WithSealPassphrase([]byte(passphrase)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	const name = "securedb"
	if _, err := n.Client().Database().CreateDatabase(ctx, transport.CreateDatabaseRequest{
		Name: name, Backend: string(database.BackendPebble), DataDir: t.TempDir(),
		Barrier: &transport.DatabaseBarrierConfig{Enabled: true, Mode: "passphrase", Secret: dbSecret},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := n.Client().Barrier().BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
	db, err := n.dbManager.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	if db.Status() != database.StatusSealed {
		t.Fatalf("database status after the node seal = %v, want sealed", db.Status())
	}

	if err := n.Client().Barrier().BarrierUnseal(ctx, &transport.BarrierUnsealRequest{Secret: []byte(passphrase)}); err != nil {
		t.Fatalf("BarrierUnseal: %v", err)
	}
	if db.Status() != database.StatusSealed {
		t.Fatalf("database status after the node unseal = %v, want still sealed (its barrier is its owner's)", db.Status())
	}
	scoped, err := n.Client().DatabaseScoped(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.KV().Get(ctx, "k"); err == nil {
		t.Fatal("the database served after the node unseal although its barrier is still sealed")
	}
}
