// Copyright (c) 2026 Jeremy Hahn
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

package standalone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// A relative trail is confined to DataDir: a symbolic link in it, at the
// trail's name or at a directory on its path, that resolves outside fails
// NewServer with *AuditTrailError, and nothing is written where it points.
// A name that climbs out of DataDir is refused the same way.
func TestNewServer_AuditTrailStaysInTheDataDir(t *testing.T) {
	ctx := context.Background()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.ndjson")
	if err := os.WriteFile(victim, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, setup := range map[string]func(dataDir string) string{
		"the trail's name": func(dataDir string) string {
			if err := os.Symlink(victim, filepath.Join(dataDir, audit.DefaultTrailFile)); err != nil {
				t.Fatal(err)
			}
			return audit.DefaultTrailFile
		},
		"a directory on its path": func(dataDir string) string {
			if err := os.Symlink(outside, filepath.Join(dataDir, "audit")); err != nil {
				t.Fatal(err)
			}
			return filepath.Join("audit", "trail.ndjson")
		},
		"a name climbing out": func(string) string {
			return filepath.Join("..", filepath.Base(outside), "climb.ndjson")
		},
	} {
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			srv, err := NewServer(ctx, &ServerConfig{Backend: database.BackendMemory, DataDir: dataDir, AuditTrail: setup(dataDir)})
			var trailErr *AuditTrailError
			if !errors.As(err, &trailErr) {
				if srv != nil {
					_ = srv.Shutdown(ctx)
				}
				t.Fatalf("a trail leaving DataDir = %v (%T), want AuditTrailError", err, err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(victim); len(entries) != 1 || err != nil || info.Size() != 0 {
				t.Fatalf("the trail reached outside DataDir: %v, %v, %v", entries, info, err)
			}
		})
	}
}
