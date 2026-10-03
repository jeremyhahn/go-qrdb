//go:build qrdb_embedded

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

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// readEmbeddedTrail returns the events in the trail file at path.
func readEmbeddedTrail(t *testing.T, path string) []audit.AuditEvent {
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

// newAuditEmbedded returns an embedded server binding over a fresh config dir.
func newAuditEmbedded(t *testing.T) *EmbeddedServerBinding {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	return NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))
}

// With the trail enabled, the embedded server opens it under the data
// directory, at qrdbd's default name, before the seal barrier: the system
// barrier's initialization is on it, keyed by the system tenant. Stopping
// closes it.
func TestEmbeddedServerBinding_AuditTrailRecordsTheBarrier(t *testing.T) {
	esb := newAuditEmbedded(t)
	dataDir := t.TempDir()
	if err := esb.StartEmbedded(&EmbeddedConfig{StorageBackend: "memory", DataDir: dataDir, Audit: &EmbeddedAuditConfig{Enabled: true}}); err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	if esb.auditTrail == nil {
		t.Fatal("the enabled trail is not held")
	}
	if err := esb.StopEmbedded(); err != nil {
		t.Fatal(err)
	}
	if esb.auditTrail != nil {
		t.Fatal("stop left the trail open")
	}
	var initialize *audit.AuditEvent
	for _, ev := range readEmbeddedTrail(t, filepath.Join(dataDir, audit.DefaultTrailFile)) {
		if ev.Action == "initialize" && ev.Resource == seal.BarrierAuditResource {
			initialize = &ev
		}
	}
	if initialize == nil || initialize.TenantID != tenantid.System || initialize.Attributes["barrier"] != seal.SystemBarrierAuditName {
		t.Fatalf("system barrier initialize record = %+v", initialize)
	}
}

// Off by default: no trail file is created.
func TestEmbeddedServerBinding_AuditTrailOffByDefault(t *testing.T) {
	esb := newAuditEmbedded(t)
	dataDir := t.TempDir()
	if err := esb.StartEmbedded(&EmbeddedConfig{StorageBackend: "memory", DataDir: dataDir}); err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = esb.StopEmbedded() })
	if esb.auditTrail != nil {
		t.Fatal("a trail was opened while off")
	}
	if _, err := os.Stat(filepath.Join(dataDir, audit.DefaultTrailFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a trail file exists while off: %v", err)
	}
}

// A configured trail that cannot be opened -- or a path outside the data
// directory -- stops the start with nothing running and nothing left open.
func TestEmbeddedServerBinding_AuditTrailFailsClosed(t *testing.T) {
	esb := newAuditEmbedded(t)
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "trail"), 0o700); err != nil {
		t.Fatal(err)
	}
	var auditErr *EmbeddedAuditError
	err := esb.StartEmbedded(&EmbeddedConfig{StorageBackend: "memory", DataDir: dataDir, Audit: &EmbeddedAuditConfig{Enabled: true, Path: "trail"}})
	if !errors.As(err, &auditErr) {
		t.Fatalf("StartEmbedded with an unopenable trail = %v (%T), want EmbeddedAuditError", err, err)
	}
	if esb.IsEmbedded() || esb.sealBarrier != nil || esb.auditTrail != nil {
		t.Fatal("a refused start left the server, its barrier or its trail behind")
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "seal")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a refused start opened a seal store: %v", statErr)
	}

	for _, path := range []string{"../escape.ndjson", "/var/log/qrdb.ndjson"} {
		var outside *AuditPathOutsideDataDirError
		err := esb.StartEmbedded(&EmbeddedConfig{StorageBackend: "memory", DataDir: dataDir, Audit: &EmbeddedAuditConfig{Enabled: true, Path: path}})
		if !errors.As(err, &outside) {
			t.Fatalf("trail path %q = %v (%T), want AuditPathOutsideDataDirError", path, err, err)
		}
	}
	if (&EmbeddedAuditError{Path: "p", Err: errors.New("x")}).Error() == "" || (&AuditPathOutsideDataDirError{Path: "p"}).Error() == "" {
		t.Fatal("empty error text")
	}
}
