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
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestManager_Create_DataDir_ReservedRoot verifies that a caller cannot
// point a database's DataDir at the manager's own data root.
func TestManager_Create_DataDir_ReservedRoot(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("reserved-root-db")
	cfg.DataDir = mgr.DataDir()

	_, err := mgr.Create(cfg)
	if err == nil {
		t.Fatal("Create() with DataDir == manager root = nil, want *DataDirReservedPathError")
	}
	var reservedErr *DataDirReservedPathError
	if !errors.As(err, &reservedErr) {
		t.Fatalf("error type = %T, want *DataDirReservedPathError", err)
	}
}

// TestManager_Create_DataDir_ReservedCatalog verifies that a caller cannot
// point a database's DataDir at the manager's shared "databases" catalog
// directory.
func TestManager_Create_DataDir_ReservedCatalog(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("reserved-catalog-db")
	cfg.DataDir = filepath.Join(mgr.DataDir(), databasesDir)

	_, err := mgr.Create(cfg)
	if err == nil {
		t.Fatal("Create() with DataDir == catalog dir = nil, want *DataDirReservedPathError")
	}
	var reservedErr *DataDirReservedPathError
	if !errors.As(err, &reservedErr) {
		t.Fatalf("error type = %T, want *DataDirReservedPathError", err)
	}
}

// TestManager_Create_DataDir_ReservedSubdir verifies that a caller cannot
// point a database's DataDir at a node-internal reserved subtree (e.g.
// "wal", "seal") under the manager's data root.
func TestManager_Create_DataDir_ReservedSubdir(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	for _, sub := range []string{"seal", "wal", "snapshot"} {
		t.Run(sub, func(t *testing.T) {
			cfg := validMemoryConfig("reserved-" + sub + "-db")
			cfg.DataDir = filepath.Join(mgr.DataDir(), sub, "nested")

			_, err := mgr.Create(cfg)
			if err == nil {
				t.Fatalf("Create() with DataDir under reserved %q = nil, want *DataDirReservedPathError", sub)
			}
			var reservedErr *DataDirReservedPathError
			if !errors.As(err, &reservedErr) {
				t.Fatalf("error type = %T, want *DataDirReservedPathError", err)
			}
		})
	}
}

// TestManager_Create_DataDir_OwnCatalogDir verifies that a database cannot
// point its own DataDir at its own catalog directory (its barrier metadata
// store), which would let application data collide with barrier
// bootstrap key material.
func TestManager_Create_DataDir_OwnCatalogDir(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	name := "self-catalog-db"
	cfg := validMemoryConfig(name)
	cfg.DataDir = mgr.dbDir("", name)

	_, err := mgr.Create(cfg)
	if err == nil {
		t.Fatal("Create() with DataDir == own dbDir = nil, want *DataDirReservedPathError")
	}
	var reservedErr *DataDirReservedPathError
	if !errors.As(err, &reservedErr) {
		t.Fatalf("error type = %T, want *DataDirReservedPathError", err)
	}
}

// TestManager_Create_DataDir_ConflictsWithOtherDatabase is the concrete
// HIGH #4 proof: a second database cannot be pointed at a first database's
// own storage directory.
func TestManager_Create_DataDir_ConflictsWithOtherDatabase(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	// Use an isolated external root (NOT a t.TempDir() call) so the
	// ancestor-direction case below tests a path that is unrelated to --
	// and specifically not an ancestor of -- the manager's own root; if it
	// were, the reserved-root check would correctly fire first and this
	// test would no longer be exercising the per-database collision path.
	victimRoot, err := os.MkdirTemp("", "qrdb-victim-root-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(victimRoot) })

	first := validMemoryConfig("victim-db")
	first.DataDir = filepath.Join(victimRoot, "victim-data")
	if _, err := mgr.Create(first); err != nil {
		t.Fatalf("Create(victim-db) error = %v", err)
	}

	// Attacker points a new database's DataDir directly at the victim's own
	// data directory.
	second := validMemoryConfig("attacker-db")
	second.DataDir = first.DataDir

	_, err = mgr.Create(second)
	if err == nil {
		t.Fatal("Create() with DataDir colliding with another database = nil, want *DataDirConflictError")
	}
	var conflictErr *DataDirConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("error type = %T, want *DataDirConflictError", err)
	}
	if conflictErr.With != "victim-db" {
		t.Errorf("DataDirConflictError.With = %q, want %q", conflictErr.With, "victim-db")
	}

	// A nested subdirectory of the victim's DataDir must be rejected too.
	third := validMemoryConfig("attacker-nested-db")
	third.DataDir = filepath.Join(first.DataDir, "nested")
	if _, err := mgr.Create(third); err == nil {
		t.Fatal("Create() with DataDir nested inside another database's DataDir = nil, want *DataDirConflictError")
	} else if !errors.As(err, &conflictErr) {
		t.Fatalf("error type = %T, want *DataDirConflictError", err)
	}

	// An ancestor of the victim's DataDir must also be rejected (the reverse
	// nesting direction).
	fourth := validMemoryConfig("attacker-ancestor-db")
	fourth.DataDir = filepath.Dir(first.DataDir)
	if _, err := mgr.Create(fourth); err == nil {
		t.Fatal("Create() with DataDir that is an ancestor of another database's DataDir = nil, want *DataDirConflictError")
	} else if !errors.As(err, &conflictErr) {
		t.Fatalf("error type = %T, want *DataDirConflictError", err)
	}

	// The rejected attacker databases must not have been left half-created.
	if _, getErr := mgr.Get("attacker-db"); getErr == nil {
		t.Error("Get(attacker-db) succeeded after a rejected create, want NotFoundError")
	}
}

// TestManager_Create_DataDir_ConflictsWithOtherDatabase_DbDir verifies that
// a new database's DataDir cannot be pointed at another database's catalog
// directory (dbDir, holding its barrier metadata) even when that
// database used the package-computed default data path (no custom DataDir
// of its own).
func TestManager_Create_DataDir_ConflictsWithOtherDatabase_DbDir(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	if _, err := mgr.Create(validMemoryConfig("default-path-db")); err != nil {
		t.Fatalf("Create(default-path-db) error = %v", err)
	}

	attacker := validMemoryConfig("attacker-dbdir-db")
	attacker.DataDir = mgr.dbDir("", "default-path-db")

	_, err := mgr.Create(attacker)
	if err == nil {
		t.Fatal("Create() with DataDir == another database's dbDir = nil, want *DataDirConflictError")
	}
	var conflictErr *DataDirConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("error type = %T, want *DataDirConflictError", err)
	}
}

// TestManager_Create_DataDir_ExternalRootAllowed verifies that a DataDir
// living entirely outside the manager's own data root -- the legitimate
// shape of a per-tenant DataRoot allocated from an independent volume pool
// -- is NOT rejected merely for being outside that root.
func TestManager_Create_DataDir_ExternalRootAllowed(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("external-root-db")
	cfg.DataDir = t.TempDir() // a distinct root, unrelated to mgr.DataDir()

	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() with an external, non-colliding DataDir returned unexpected error: %v", err)
	}
}

// TestManager_Create_DataDir_SymlinkEscape verifies that a symlink planted
// inside an otherwise-plausible DataDir cannot be used to defeat the
// reserved-path check by resolving to the manager's own root.
func TestManager_Create_DataDir_SymlinkEscape(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	parent := t.TempDir()
	link := filepath.Join(parent, "escape-link")
	if err := os.Symlink(mgr.DataDir(), link); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	// The symlink itself resolves exactly to the manager's own root, so a
	// DataDir that is nothing but the symlink path must be caught by the
	// same reserved-root check as if the caller had spelled out the real
	// path -- proving symlink resolution, not literal path comparison, is
	// what's enforced.
	cfg := validMemoryConfig("symlink-escape-db")
	cfg.DataDir = link

	_, err := mgr.Create(cfg)
	if err == nil {
		t.Fatal("Create() with a DataDir that symlink-resolves into the manager root = nil, want *DataDirReservedPathError")
	}
	var reservedErr *DataDirReservedPathError
	if !errors.As(err, &reservedErr) {
		t.Fatalf("error type = %T, want *DataDirReservedPathError", err)
	}
}

// TestManager_Open_DataDir_RevalidatesReservedPath verifies that Open fails
// closed when on-disk metadata (predating this validation, or tampered
// with) records a DataDir that collides with a reserved path, rather than
// silently trusting it.
func TestManager_Open_DataDir_RevalidatesReservedPath(t *testing.T) {
	dataDir := t.TempDir()
	mgr := newCatalogManager(nil, dataDir)
	defer func() { _ = mgr.Close() }()

	// Create normally first so the database's catalog directory exists,
	// then simulate on-disk metadata tampering (or a pre-validation-era
	// record) by overwriting its catalog record with a reserved-path DataDir.
	if _, err := mgr.Create(validMemoryConfig("tampered-datadir-db")); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	mgr = newCatalogManager(nil, dataDir)
	defer func() { _ = mgr.Close() }()

	meta := DatabaseMeta{
		Name:    "tampered-datadir-db",
		Backend: BackendMemory,
		DataDir: filepath.Join(dataDir, databasesDir), // the reserved databases root
	}
	if err := testCatalogFor(mgr.dataDir).Update(context.Background(), meta.TenantID, meta.Name, meta.Incarnation, replaceRecord(meta)); err != nil {
		t.Fatalf("catalog Update: %v", err)
	}

	_, err := mgr.Open(context.Background(), "", "tampered-datadir-db")
	if err == nil {
		t.Fatal("Open() with a tampered reserved-path DataDir = nil, want *DataDirReservedPathError")
	}
	var reservedErr *DataDirReservedPathError
	if !errors.As(err, &reservedErr) {
		t.Fatalf("error type = %T, want *DataDirReservedPathError", err)
	}
}

// TestManager_Create_DataDir_Empty_AlwaysValid verifies that the common
// case -- no custom DataDir at all -- is never rejected by validateDataDir.
func TestManager_Create_DataDir_Empty_AlwaysValid(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	if err := mgr.validateDataDir("", "any-db", ""); err != nil {
		t.Errorf("validateDataDir(\"\") returned unexpected error: %v", err)
	}
}

// TestPathContains covers the pathContains helper directly, including the
// path-segment-boundary edge case (a naive string-prefix check would wrongly
// treat "/a/b2" as nested inside "/a/b").
func TestPathContains(t *testing.T) {
	tests := []struct {
		base      string
		candidate string
		want      bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/b/c", true},
		{"/a/b", "/a/b2", false},
		{"/a/b", "/a", false},
		{"/a/b", "/c/d", false},
	}
	for _, tt := range tests {
		if got := pathContains(tt.base, tt.candidate); got != tt.want {
			t.Errorf("pathContains(%q, %q) = %v, want %v", tt.base, tt.candidate, got, tt.want)
		}
	}
}
