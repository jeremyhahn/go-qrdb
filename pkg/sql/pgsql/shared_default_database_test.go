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

//go:build qrdb_pgsql

package pgsql

import "testing"

// The configured default SQL database is shared with every tenant only when
// the server is told so; the name alone shares nothing.
func TestWithSharedDefaultDatabase(t *testing.T) {
	shared := NewServer(nil, nil, "127.0.0.1:0", WithDatabaseName("qrdb"), WithSharedDefaultDatabase("qrdb"))
	if shared.sharedDefaultDB != "qrdb" || shared.dbName != "qrdb" {
		t.Fatalf("shared = %q (name %q), want qrdb", shared.sharedDefaultDB, shared.dbName)
	}
	named := NewServer(nil, nil, "127.0.0.1:0", WithDatabaseName("qrdb"))
	if named.sharedDefaultDB != "" {
		t.Fatalf("a name alone shared %q", named.sharedDefaultDB)
	}
}
