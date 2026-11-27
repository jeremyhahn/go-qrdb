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

package standalone

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// dirNames returns the sorted names of the entries in dir.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// A raft-marked data directory is refused before the server writes under it:
// no storage, seal store or audit trail appears (#658).
func TestNewServer_RunModeRefusesARaftDirectory(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := lifecycle.ClaimRunMode(dataDir, capabilities.ModeRaft); err != nil {
		t.Fatal(err)
	}
	before := dirNames(t, dataDir)
	srv, err := NewServer(ctx, &ServerConfig{Backend: database.BackendPebble, DataDir: dataDir, AuditTrail: audit.DefaultTrailFile})
	var mm *lifecycle.RunModeMismatchError
	if !errors.As(err, &mm) {
		if srv != nil {
			_ = srv.Shutdown(ctx)
		}
		t.Fatalf("NewServer on a raft directory = %v (%T), want *lifecycle.RunModeMismatchError", err, err)
	}
	if after := dirNames(t, dataDir); !slices.Equal(before, after) {
		t.Fatalf("the refused start wrote under the data directory: before %v, after %v", before, after)
	}
}

// A fresh data directory is marked standalone, so a later raft start on it
// is refused (#658).
func TestNewServer_RunModeMarksAFreshDirectory(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	srv, err := NewServer(ctx, &ServerConfig{Backend: database.BackendPebble, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	var mm *lifecycle.RunModeMismatchError
	if err := lifecycle.ClaimRunMode(dataDir, capabilities.ModeRaft); !errors.As(err, &mm) {
		t.Fatalf("raft claim after a standalone server = %v (%T), want *lifecycle.RunModeMismatchError", err, err)
	}
}
