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

package database

import (
	"context"
	"testing"
)

// TestManager_ConfigUpdateKeepsTheDataDirectory pins that rewriting a
// database's catalog record after a configuration update keeps its dedicated data
// directory. Dropping it reopened the database on the shared default store
// after the next restart: its data out of reach, and an encrypted database
// refused outright (a barrier needs a dedicated store).
func TestManager_ConfigUpdateKeepsTheDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	mgr := newCatalogManager(nil, dataDir)
	cfg := validDedicatedMemoryConfig(t, "dedicated")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpdateProtocol("dedicated", ProtocolConfig{MySQL: true}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := newCatalogManager(nil, dataDir)
	defer func() { _ = reopened.Close() }()
	db, err := reopened.Open(context.Background(), "", "dedicated")
	if err != nil {
		t.Fatal(err)
	}
	if got := db.Config().DataDir; got != cfg.DataDir {
		t.Fatalf("reopened DataDir = %q, want %q", got, cfg.DataDir)
	}
	if !db.Config().Protocol.MySQL {
		t.Fatal("the update itself was not kept")
	}
}
