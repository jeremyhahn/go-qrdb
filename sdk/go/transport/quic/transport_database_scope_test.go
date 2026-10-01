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

package quic

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTransportImplementsDatabaseScoper asserts the QUIC transport satisfies
// the DatabaseScoper interface so Client.DatabaseScoped dispatches to it.
func TestTransportImplementsDatabaseScoper(t *testing.T) {
	var tr any = &Transport{}
	if _, ok := tr.(transport.DatabaseScoper); !ok {
		t.Fatal("QUIC Transport does not implement transport.DatabaseScoper")
	}
}

// TestWithDatabasePropagatesScope verifies that a database-scoped QUIC
// transport carries the database name on the data-plane request frame.
func TestWithDatabasePropagatesScope(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.Put", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})

	scoped, ok := tr.withDatabase("db2"), true
	if !ok {
		t.Fatal("WithDatabase did not return a *Transport")
	}

	if err := scoped.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	last := srv.LastRequest("kv.Put")
	if last == nil {
		t.Fatal("server did not record a kv.Put request")
	}
	if last.Database != "db2" {
		t.Errorf("frame database = %q, want %q", last.Database, "db2")
	}
}

// TestWithDatabaseSystemIsUnscoped verifies that scoping to the default
// "system" database (or the empty string) sends no database on the wire,
// matching the unscoped behavior.
func TestWithDatabaseSystemIsUnscoped(t *testing.T) {
	cases := []struct {
		name     string
		database string
	}{
		{"explicit system", "system"},
		{"empty string", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, tr := startE2EServer(t)
			srv.SetResponder("kv.Put", func(_ []byte) (uint16, []byte) {
				return wire.StatusOK, nil
			})

			scoped, ok := tr.withDatabase(tc.database), true
			if !ok {
				t.Fatal("WithDatabase did not return a *Transport")
			}
			if err := scoped.Put(context.Background(), "k", []byte("v")); err != nil {
				t.Fatalf("Put: %v", err)
			}

			last := srv.LastRequest("kv.Put")
			if last == nil {
				t.Fatal("server did not record a kv.Put request")
			}
			if last.Database != "" {
				t.Errorf("frame database = %q, want empty", last.Database)
			}
		})
	}
}

// TestUnscopedTransportSendsNoDatabase verifies the parent (unscoped)
// transport sends an empty database field.
func TestUnscopedTransportSendsNoDatabase(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})

	if _, err := tr.Get(context.Background(), "k"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	last := srv.LastRequest("kv.Get")
	if last == nil {
		t.Fatal("server did not record a kv.Get request")
	}
	if last.Database != "" {
		t.Errorf("frame database = %q, want empty", last.Database)
	}
}
