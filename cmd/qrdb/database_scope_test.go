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
	"errors"
	"strings"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// scopeClientToDatabase — success path (all five wire transports implement
// transport.DatabaseScoper; scoping never requires a live connection) and
// the typed-error surface for the embedded transport, which does not.
// ---------------------------------------------------------------------------

// newWireScopeTestClient builds an unconnected REST-transport client.
// DatabaseScoped only wraps the transport, so no server is needed.
func newWireScopeTestClient(t *testing.T) *sdk.Client {
	t.Helper()

	tr, err := newTransport(transportREST, transport.WithAddress("https://127.0.0.1:1"))
	if err != nil {
		t.Fatalf("newTransport: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })
	return sdkClient
}

func TestScopeClientToDatabase_Success(t *testing.T) {
	c := newWireScopeTestClient(t)

	scoped, err := scopeClientToDatabase(c, "analytics", transportREST)
	if err != nil {
		t.Fatalf("scopeClientToDatabase: %v", err)
	}
	if scoped == nil {
		t.Fatal("scoped client is nil")
	}
	if scoped == c {
		t.Fatal("scoped client should be a new database-scoped client, got the original")
	}
}

func TestScopeClientToDatabase_DistinctScopesDistinctClients(t *testing.T) {
	c := newWireScopeTestClient(t)

	a, err := scopeClientToDatabase(c, "db-a", transportREST)
	if err != nil {
		t.Fatalf("scope db-a: %v", err)
	}
	b, err := scopeClientToDatabase(c, "db-b", transportREST)
	if err != nil {
		t.Fatalf("scope db-b: %v", err)
	}
	if a == b {
		t.Fatal("scoping to different databases returned the same client")
	}
}

func TestScopeClientToDatabase_EmbeddedUnsupported(t *testing.T) {
	tr, err := embedded.New(&noopKVService{}, newMockAdminService())
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	c, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	_, err = scopeClientToDatabase(c, "analytics", transportEmbedded)
	var scopeErr *DatabaseScopeError
	if !errors.As(err, &scopeErr) {
		t.Fatalf("expected *DatabaseScopeError, got %T: %v", err, err)
	}
	if scopeErr.Database != "analytics" || scopeErr.Transport != transportEmbedded {
		t.Fatalf("unexpected error fields: %+v", scopeErr)
	}
}

func TestDatabaseScopeError_Message(t *testing.T) {
	err := &DatabaseScopeError{Database: "analytics", Transport: "rest"}
	msg := err.Error()
	for _, want := range []string{"qrdb:", "rest", "analytics"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("DatabaseScopeError message %q missing %q", msg, want)
		}
	}
}

// ---------------------------------------------------------------------------
// --database global flag — registration and validation-time behavior.
// ---------------------------------------------------------------------------

func TestRootCmd_DatabaseFlagRegistered(t *testing.T) {
	root := newRootCmd()
	f := root.PersistentFlags().Lookup("database")
	if f == nil {
		t.Fatal("--database persistent flag not registered")
	}
	if f.DefValue != "" {
		t.Fatalf("--database default = %q, want empty (server default database)", f.DefValue)
	}
}

func TestRootCmd_DatabaseFlagSkippedForVersion(t *testing.T) {
	// version bypasses PersistentPreRunE entirely, so --database must not
	// trigger client construction or scoping.
	root := newRootCmd()
	root.SetArgs([]string{"version", "--database", "analytics"})
	if err := root.Execute(); err != nil {
		t.Fatalf("qrdb version --database: %v", err)
	}
}
