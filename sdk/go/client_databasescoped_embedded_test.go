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

package sdk_test

// The embedded transport imports this package, so a test that drives it must
// live in the external test package: package sdk cannot import it back.

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// scopedKVRegistry resolves KV for one (tenant, database) pair to its own
// store, as the node's registry does; only the KV resolver is driven.
type scopedKVRegistry struct {
	service.Registry
	db    string
	store *mockKVService
}

func (r *scopedKVRegistry) KVService(_, db string) (service.KVServicer, error) {
	if db != r.db {
		return nil, &service.DatabaseNotFoundError{Database: db}
	}
	return r.store, nil
}

// TestDatabaseScoped_Embedded pins the embedded transport end to end. It did
// not implement DatabaseScoper, and DatabaseScoped returned the receiver, so
// a caller asking for database "db2" silently wrote the node's default
// database. Without a service registry it must now refuse; with one the
// write must land in db2 and nowhere else.
func TestDatabaseScoped_Embedded(t *testing.T) {
	ctx := context.Background()

	t.Run("no registry refuses", func(t *testing.T) {
		defaultKV := newMockKVService()
		tr, err := embedded.New(defaultKV, &mockAdminService{})
		if err != nil {
			t.Fatalf("embedded.New: %v", err)
		}
		c, _ := sdk.NewClient(tr)
		scoped, err := c.DatabaseScoped("db2")
		var unsupported *transport.DatabaseScopeUnsupportedError
		if !errors.As(err, &unsupported) || scoped != nil {
			t.Fatalf("DatabaseScoped over embedded without a registry = %v, %v; want a refusal", scoped, err)
		}
		if len(defaultKV.store) != 0 {
			t.Fatalf("the default database was touched: %v", defaultKV.store)
		}
	})

	t.Run("registry resolves the named database", func(t *testing.T) {
		defaultKV, db2 := newMockKVService(), newMockKVService()
		tr, err := embedded.New(defaultKV, &mockAdminService{},
			embedded.WithServiceRegistry(&scopedKVRegistry{db: "db2", store: db2}))
		if err != nil {
			t.Fatalf("embedded.New: %v", err)
		}
		c, _ := sdk.NewClient(tr)
		scoped, err := c.DatabaseScoped("db2")
		if err != nil {
			t.Fatalf("DatabaseScoped: %v", err)
		}
		if err := scoped.KV().Put(ctx, "k", []byte("v")); err != nil {
			t.Fatalf("scoped Put: %v", err)
		}
		if _, ok := defaultKV.store["k"]; ok {
			t.Fatal("a write scoped to db2 landed in the default database")
		}
		if _, ok := db2.store["k"]; !ok {
			t.Fatal("a write scoped to db2 did not land in db2")
		}
	})
}
