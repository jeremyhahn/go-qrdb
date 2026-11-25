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

package service

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestDatabaseService_DropLeavesACallerChosenDataDirInPlace drops a barrier
// database whose data directory the caller chose. Dropping reclaims only what
// qrdb created under its own data root; the caller's directory, and a file the
// caller put in it, are left in place, as PostgreSQL's DROP TABLESPACE leaves
// the tablespace's directory.
func TestDatabaseService_DropLeavesACallerChosenDataDirInPlace(t *testing.T) {
	svc, _ := newTestDatabaseServiceWithBarrier(t)
	ctx := context.Background()

	callerDir := filepath.Join(t.TempDir(), "caller-chosen")
	if err := os.MkdirAll(callerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(callerDir, "caller-owned.txt")
	want := []byte("the caller's own file")
	if err := os.WriteFile(sentinel, want, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := testDBConfig("drop-dir-db")
	cfg.DataDir = callerDir
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}
	if _, err := svc.CreateWithCredentials(ctx, cfg, seal.MustCredentials(ctx, []byte("s3cret"))); err != nil {
		t.Fatalf("CreateWithCredentials() error = %v", err)
	}

	if err := svc.Drop(ctx, "drop-dir-db"); err != nil {
		t.Fatalf("Drop() error = %v", err)
	}

	info, err := os.Stat(callerDir)
	if err != nil {
		t.Fatalf("caller-chosen data dir after Drop(): %v; want it left in place", err)
	}
	if !info.IsDir() {
		t.Fatalf("caller-chosen data dir after Drop() is not a directory")
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("caller's file after Drop(): %v; want it left in place", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("caller's file after Drop() = %q, want %q", got, want)
	}
	if _, err := svc.Get("drop-dir-db"); err == nil {
		t.Fatal("Get() after Drop() = nil error, want the database gone")
	}
}
