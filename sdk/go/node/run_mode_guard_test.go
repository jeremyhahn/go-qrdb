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

package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

func runModeGuardDir(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	if err := lifecycle.ClaimRunMode(dir, mode); err != nil {
		t.Fatalf("ClaimRunMode: %v", err)
	}
	return dir
}

func requireOnlyMarker(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	// The seeding claim leaves the marker and the lock file it claimed under;
	// anything else was written by the refused start.
	for _, e := range entries {
		if e.Name() != ".qrdb-run-mode" && e.Name() != ".qrdb-run-mode.lock" {
			t.Fatalf("refused start created %s in the data directory", e.Name())
		}
	}
}

func TestRunModeGuard_StandaloneRefusesRaftDataDir(t *testing.T) {
	dir := runModeGuardDir(t, capabilities.ModeRaft)
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = dir
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: "pebble"}}

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	if n != nil {
		t.Cleanup(func() { _ = n.Close() })
	}
	var mm *lifecycle.RunModeMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("err = %v (%T), want *lifecycle.RunModeMismatchError", err, err)
	}
	if mm.Recorded != capabilities.ModeRaft || mm.Configured != capabilities.ModeStandalone {
		t.Fatalf("got %+v", mm)
	}
	requireOnlyMarker(t, dir)
}

func TestRunModeGuard_ClusteredRefusesStandaloneDataDir(t *testing.T) {
	dir := runModeGuardDir(t, capabilities.ModeStandalone)
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = dir

	n, err := NewClusteredNode(context.Background(), cfg, map[uint64]string{1: "127.0.0.1:1"}, nil)
	if n != nil {
		t.Cleanup(func() { _ = n.Close() })
	}
	var mm *lifecycle.RunModeMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("err = %v (%T), want *lifecycle.RunModeMismatchError", err, err)
	}
	if mm.Recorded != capabilities.ModeStandalone || mm.Configured != capabilities.ModeRaft {
		t.Fatalf("got %+v", mm)
	}
	for _, d := range []string{"wal", "nodehost"} {
		if _, err := os.Stat(filepath.Join(dir, d)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refused start created %s/", d)
		}
	}
	requireOnlyMarker(t, dir)
}

func TestRunModeGuard_MemoryBackendWritesNoMarker(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = ""
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: "memory"}}

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	if err == nil {
		t.Cleanup(func() { _ = n.Close() })
	}
	if _, serr := os.Stat(filepath.Join(cwd, ".qrdb-run-mode")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("memory backend wrote a run-mode marker (start err %v)", err)
	}
}
