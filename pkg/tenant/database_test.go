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

package tenant

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// ComposeNamespace
// ---------------------------------------------------------------------------

func TestComposeNamespace(t *testing.T) {
	cases := []struct {
		tenant, db, want string
	}{
		{"system", "system", "system/system/"}, // bootstrap pair is prefixed
		{"system", "", "system/system/"},       // empty db falls back to DefaultDatabase
		{"", "", "system/system/"},             // empty tenant+db == bootstrap pair
		{"system", "reports", "system/reports/"},
		{"acme", "system", "acme/system/"},
		{"acme", "", "acme/system/"}, // empty db falls back to DefaultDatabase, not the tenant's own name
		{"acme", "reports", "acme/reports/"},
	}
	for _, c := range cases {
		got := ComposeNamespace(c.tenant, c.db)
		if got != c.want {
			t.Errorf("ComposeNamespace(%q,%q)=%q, want %q", c.tenant, c.db, got, c.want)
		}
	}
}

// TestComposeNamespace_FallbacksUseCanonicalConstants pins the L5 fix: the
// empty-input fallbacks are the package's own tenantid.System/DefaultDatabase
// constants, not caller-supplied values — this is what makes ComposeNamespace
// the single canonical formula instead of one of two independently-configured
// implementations that only happened to agree.
func TestComposeNamespace_FallbacksUseCanonicalConstants(t *testing.T) {
	want := tenantid.System + "/" + DefaultDatabase + "/"
	if got := ComposeNamespace("", ""); got != want {
		t.Fatalf("ComposeNamespace(\"\",\"\") = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// WithDatabase / DatabaseFromContext
// ---------------------------------------------------------------------------

func TestDatabaseFromContext_DefaultsWhenUnset(t *testing.T) {
	if got := DatabaseFromContext(context.Background()); got != DefaultDatabase {
		t.Errorf("DatabaseFromContext(unset) = %q, want %q", got, DefaultDatabase)
	}
}

func TestDatabaseFromContext_DefaultsWhenEmpty(t *testing.T) {
	ctx := WithDatabase(context.Background(), "")
	if got := DatabaseFromContext(ctx); got != DefaultDatabase {
		t.Errorf("DatabaseFromContext(empty) = %q, want %q", got, DefaultDatabase)
	}
}

func TestDatabaseFromContext_RoundTrips(t *testing.T) {
	ctx := WithDatabase(context.Background(), "reports")
	if got := DatabaseFromContext(ctx); got != "reports" {
		t.Errorf("DatabaseFromContext = %q, want %q", got, "reports")
	}
}
