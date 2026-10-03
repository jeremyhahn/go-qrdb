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

package embedded

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// readServerTrail returns the events of the trail file at path.
func readServerTrail(t *testing.T, path string) []audit.AuditEvent {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("the trail exists: %v", err)
	}
	defer func() { _ = f.Close() }()
	var out []audit.AuditEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var ev audit.AuditEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

// An embedded server with a trail records its barrier's events on it, keyed
// by the system tenant: the first boot's initialize, the second boot's
// unseal. The passphrase never reaches the trail.
func TestNewServer_AuditTrailRecordsTheUnseal(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	const secret = "embedded-audit-trail-passphrase"
	cfg := func() *ServerConfig {
		return &ServerConfig{
			Backend:    database.BackendPebble,
			DataDir:    dataDir,
			Barrier:    &BarrierConfig{Strategy: seal.StrategyPassphrase, AutoUnseal: true, Passphrase: barrierPassphrase(t, secret)},
			AuditTrail: audit.DefaultTrailFile,
		}
	}
	for boot := 0; boot < 2; boot++ {
		srv, err := NewServer(ctx, cfg())
		if err != nil {
			t.Fatalf("NewServer (boot %d): %v", boot, err)
		}
		if srv.trail == nil {
			t.Fatal("the configured trail is not held")
		}
		if err := srv.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		if srv.trail != nil {
			t.Fatal("Shutdown left the trail open")
		}
	}
	path := filepath.Join(dataDir, audit.DefaultTrailFile)
	seen := map[string]bool{}
	for _, ev := range readServerTrail(t, path) {
		if ev.Resource != seal.BarrierAuditResource {
			continue
		}
		if ev.TenantID != tenantid.System || ev.Attributes["barrier"] != seal.SystemBarrierAuditName {
			t.Fatalf("barrier record = %+v", ev)
		}
		if ev.Outcome == audit.OutcomeSuccess {
			seen[ev.Action] = true
		}
	}
	if !seen["initialize"] || !seen["unseal"] {
		t.Fatalf("barrier records = %v, want the first boot's initialize and the second boot's unseal", seen)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("the passphrase reached the trail")
	}
}

// Without a trail configured -- the default -- no trail file exists.
func TestNewServer_NoAuditTrailByDefault(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	srv, err := NewServer(ctx, &ServerConfig{Backend: database.BackendPebble, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(ctx) })
	if srv.trail != nil {
		t.Fatal("a trail was opened while off")
	}
	if _, err := os.Stat(filepath.Join(dataDir, audit.DefaultTrailFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a trail file exists while off: %v", err)
	}
}

// A configured trail that cannot be opened fails NewServer with nothing
// started; a relative path needs a data directory.
func TestNewServer_AuditTrailFailsClosed(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "trail"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := NewServer(ctx, &ServerConfig{
		Backend:    database.BackendPebble,
		DataDir:    dataDir,
		Barrier:    &BarrierConfig{Strategy: seal.StrategyPassphrase, AutoUnseal: true, Passphrase: barrierPassphrase(t, "fail-closed-passphrase")},
		AuditTrail: "trail",
	})
	var trailErr *AuditTrailError
	if !errors.As(err, &trailErr) || trailErr.Error() == "" || trailErr.Unwrap() == nil {
		t.Fatalf("NewServer with an unopenable trail = %v (%T), want AuditTrailError", err, err)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "seal")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a refused start opened the seal store: %v", statErr)
	}

	_, err = NewServer(ctx, &ServerConfig{Backend: database.BackendMemory, AuditTrail: "relative.ndjson"})
	var cfgErr *ServerConfigError
	if !errors.As(err, &cfgErr) || cfgErr.Field != "AuditTrail" {
		t.Fatalf("a relative trail with no data directory = %v (%T)", err, err)
	}

	abs := filepath.Join(t.TempDir(), "abs.ndjson")
	srv, err := NewServer(ctx, &ServerConfig{Backend: database.BackendMemory, AuditTrail: abs})
	if err != nil {
		t.Fatalf("an absolute trail path = %v", err)
	}
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("the absolute trail was not opened: %v", err)
	}
}
