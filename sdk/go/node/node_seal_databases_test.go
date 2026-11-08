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
	"bytes"
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

const (
	nodeSealPass = "whole-node-passphrase"
	dbSealPass   = "db-pass-123456"
)

// newNodeWithPassphraseDatabase boots a sealed standalone node, unsealed with
// nodeSealPass, holding one passphrase database "secure".
func newNodeWithPassphraseDatabase(t *testing.T) *Node {
	t.Helper()
	ctx := context.Background()
	n, err := NewStandaloneNode(ctx, sealedStandaloneConfig(t, t.TempDir()), nil, WithSealPassphrase([]byte(nodeSealPass)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	if _, err := n.Client().Database().CreateDatabase(ctx, transport.CreateDatabaseRequest{
		Name: "secure", Backend: string(database.BackendPebble), DataDir: t.TempDir(),
		Barrier: &transport.DatabaseBarrierConfig{Enabled: true, Mode: "passphrase", Secret: dbSealPass},
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// databaseKeyWiped reports whether the database barrier holds no key.
func databaseKeyWiped(t *testing.T, n *Node, name string) bool {
	t.Helper()
	bm := n.dbManager.BarrierManager()
	db, err := n.dbManager.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	st, err := bm.BarrierStatus(name, db.TenantID())
	if err != nil {
		t.Fatal(err)
	}
	enc := bm.GetBarrier(name, db.TenantID())
	return st.Sealed && enc != nil && enc.IsSealed()
}

// TestNodeSeal_WipesDatabaseKeysAndTheOwnerUnsealsAfter (the reviewer's
// probe): a node seal wipes a passphrase database's key, not only its status
// flag; after the node unseals the database stays sealed for its owner, whose
// unseal then works and serves a write and a read.
func TestNodeSeal_WipesDatabaseKeysAndTheOwnerUnsealsAfter(t *testing.T) {
	ctx := context.Background()
	n := newNodeWithPassphraseDatabase(t)

	if err := n.Client().Barrier().BarrierSeal(ctx); err != nil {
		t.Fatal(err)
	}
	if !databaseKeyWiped(t, n, "secure") {
		t.Fatal("the node reports sealed but the database barrier still holds its key")
	}
	if err := n.Client().Barrier().BarrierUnseal(ctx, &transport.BarrierUnsealRequest{Secret: []byte(nodeSealPass)}); err != nil {
		t.Fatal(err)
	}
	if !databaseKeyWiped(t, n, "secure") {
		t.Fatal("the node unseal unsealed an owner-managed database")
	}
	if err := n.Client().Database().UnsealDatabase(ctx, "secure", dbSealPass); err != nil {
		t.Fatalf("owner UnsealDatabase after a node seal and unseal: %v", err)
	}
	scoped, err := n.Client().DatabaseScoped("secure")
	if err != nil {
		t.Fatal(err)
	}
	if err := scoped.KV().Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put after the owner's unseal: %v", err)
	}
	got, err := scoped.KV().Get(ctx, "k")
	if err != nil || !bytes.Equal(got, []byte("v")) {
		t.Fatalf("Get after the owner's unseal = %q, %v", got, err)
	}
}

// TestNodeSeal_OwnerSealIsNotUndoneByTheNodeUnseal: a database its owner
// sealed before the node seal stays sealed through the node unseal.
func TestNodeSeal_OwnerSealIsNotUndoneByTheNodeUnseal(t *testing.T) {
	ctx := context.Background()
	n := newNodeWithPassphraseDatabase(t)
	if err := n.Client().Database().SealDatabase(ctx, "secure"); err != nil {
		t.Fatal(err)
	}
	if err := n.Client().Barrier().BarrierSeal(ctx); err != nil {
		t.Fatal(err)
	}
	if err := n.Client().Barrier().BarrierUnseal(ctx, &transport.BarrierUnsealRequest{Secret: []byte(nodeSealPass)}); err != nil {
		t.Fatal(err)
	}
	if !databaseKeyWiped(t, n, "secure") {
		t.Fatal("the node unseal undid the owner's seal")
	}
	if err := n.Client().Database().UnsealDatabase(ctx, "secure", dbSealPass); err != nil {
		t.Fatalf("owner UnsealDatabase: %v", err)
	}
}
