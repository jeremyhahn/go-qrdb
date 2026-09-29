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

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// scoperTransport is a minimal transport that records the database it was
// scoped to and implements transport.DatabaseScoper.
type scoperTransport struct {
	database string
	refuse   error
}

func (s *scoperTransport) Connect(context.Context) error { return nil }
func (s *scoperTransport) Close() error                  { return nil }
func (s *scoperTransport) Healthy(context.Context) bool  { return true }
func (s *scoperTransport) Request(context.Context, string, any, any) error {
	return nil
}

func (s *scoperTransport) WithDatabase(database string) (transport.Transport, error) {
	if s.refuse != nil {
		return nil, s.refuse
	}
	return &scoperTransport{database: transport.NormalizeDatabaseScope(database)}, nil
}

// plainTransport does NOT implement DatabaseScoper.
type plainTransport struct{}

func (plainTransport) Connect(context.Context) error                   { return nil }
func (plainTransport) Close() error                                    { return nil }
func (plainTransport) Healthy(context.Context) bool                    { return true }
func (plainTransport) Request(context.Context, string, any, any) error { return nil }

func TestDatabaseScoped_DelegatesToTransport(t *testing.T) {
	c, err := sdk.NewClient(&scoperTransport{})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	scoped, err := c.DatabaseScoped("db2")
	if err != nil {
		t.Fatalf("DatabaseScoped: %v", err)
	}
	if scoped == c {
		t.Fatal("expected a distinct scoped client")
	}
	st, ok := scoped.TransportForTest().(*scoperTransport)
	if !ok {
		t.Fatalf("unexpected transport type %T", scoped.TransportForTest())
	}
	if st.database != "db2" {
		t.Fatalf("expected db2, got %q", st.database)
	}
}

func TestDatabaseScoped_SystemIsTheReceiver(t *testing.T) {
	for _, tr := range []transport.Transport{&scoperTransport{}, plainTransport{}} {
		c, _ := sdk.NewClient(tr)
		for _, name := range []string{"", "system"} {
			scoped, err := c.DatabaseScoped(name)
			if err != nil || scoped != c {
				t.Fatalf("DatabaseScoped(%q) over %T = %v, %v; want the receiver", name, tr, scoped, err)
			}
		}
	}
}

// A transport that cannot scope is refused, never answered with the default
// database under the requested name.
func TestDatabaseScoped_UnsupportedTransportRefuses(t *testing.T) {
	c, _ := sdk.NewClient(plainTransport{})
	scoped, err := c.DatabaseScoped("db2")
	var unsupported *transport.DatabaseScopeUnsupportedError
	if !errors.As(err, &unsupported) || scoped != nil {
		t.Fatalf("DatabaseScoped over a non-scoper = %v, %v; want *transport.DatabaseScopeUnsupportedError", scoped, err)
	}
}

func TestDatabaseScoped_TransportRefusalPropagates(t *testing.T) {
	refusal := &transport.DatabaseScopeUnsupportedError{Database: "db2", Reason: "test"}
	c, _ := sdk.NewClient(&scoperTransport{refuse: refusal})
	if _, err := c.DatabaseScoped("db2"); !errors.Is(err, refusal) {
		t.Fatalf("err = %v, want the transport's refusal", err)
	}
}

func TestDatabaseScoped_ClosedClient(t *testing.T) {
	c, _ := sdk.NewClient(&scoperTransport{})
	_ = c.Close()
	var closed *sdk.ClientClosedError
	if _, err := c.DatabaseScoped("db2"); !errors.As(err, &closed) {
		t.Fatalf("err = %v, want *sdk.ClientClosedError", err)
	}
}

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
