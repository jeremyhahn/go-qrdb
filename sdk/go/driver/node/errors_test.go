//go:build qrdb_sql

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
	"strings"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	qrdbdriver "github.com/jeremyhahn/go-qrdb/sdk/go/driver"
)

// errSentinel is the underlying cause used by the wrapping-error tests.
var errSentinel = errors.New("underlying cause")

// TestStartError verifies the message names the database and the cause, and
// that the cause stays reachable through errors.Is.
func TestStartError(t *testing.T) {
	err := &StartError{Database: "mydb", Err: errSentinel}

	msg := err.Error()
	if !strings.Contains(msg, `"mydb"`) {
		t.Errorf("Error() = %q, want it to quote the database name", msg)
	}
	if !strings.Contains(msg, errSentinel.Error()) {
		t.Errorf("Error() = %q, want it to include the cause", msg)
	}
	if !errors.Is(err, errSentinel) {
		t.Error("errors.Is must reach the wrapped cause")
	}
}

// TestDataDirError verifies the message and unwrapping of the scratch
// directory failure.
func TestDataDirError(t *testing.T) {
	err := &DataDirError{Err: errSentinel}

	if !strings.Contains(err.Error(), errSentinel.Error()) {
		t.Errorf("Error() = %q, want it to include the cause", err.Error())
	}
	if !errors.Is(err, errSentinel) {
		t.Error("errors.Is must reach the wrapped cause")
	}
}

// TestSealedError verifies the sealed-node message names the database and
// explains why the driver cannot proceed.
func TestSealedError(t *testing.T) {
	msg := (&SealedError{Database: "sealeddb"}).Error()

	if !strings.Contains(msg, `"sealeddb"`) {
		t.Errorf("Error() = %q, want it to quote the database name", msg)
	}
	if !strings.Contains(msg, "unseal credentials") {
		t.Errorf("Error() = %q, want it to explain the missing credentials", msg)
	}
}

// TestRegistryUnavailableError verifies the message states plainly that the
// node produced no registry and that the driver refuses to degrade.
func TestRegistryUnavailableError(t *testing.T) {
	msg := (&RegistryUnavailableError{Database: "regless"}).Error()

	if !strings.Contains(msg, `"regless"`) {
		t.Errorf("Error() = %q, want it to quote the database name", msg)
	}
	if !strings.Contains(msg, "no service registry") {
		t.Errorf("Error() = %q, want it to say the node produced no registry", msg)
	}
	if !strings.Contains(msg, "KV-only") {
		t.Errorf("Error() = %q, want it to say it refuses a KV-only connection", msg)
	}
}

// TestConnector_StartFailureIsTyped verifies a node that cannot boot surfaces
// *StartError rather than a bare failure from deep inside the node builder.
func TestConnector_StartFailureIsTyped(t *testing.T) {
	// A regular file where the node expects to root its data directory: the
	// node builder fails while creating storage under it.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := qrdbdriver.ParseDSN("node://" + blocker + "?backend=pebble&database=broken")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	c := New(cfg)
	defer func() { _ = c.Close() }()

	_, err = c.Connect(context.Background())
	if err == nil {
		t.Fatal("expected Connect to fail when the data directory is a file")
	}
	var startErr *StartError
	if !errors.As(err, &startErr) {
		t.Fatalf("error = %v (%T), want *StartError", err, err)
	}
	if startErr.Database != "broken" {
		t.Errorf("StartError.Database = %q, want %q", startErr.Database, "broken")
	}
}

// TestConnector_ScratchDirFailureIsTyped verifies a scratch directory that
// cannot be created surfaces *DataDirError.
func TestConnector_ScratchDirFailureIsTyped(t *testing.T) {
	// os.MkdirTemp roots at os.TempDir(), which honours TMPDIR. Pointing it at
	// a path that does not exist makes the creation fail deterministically
	// without touching any real directory.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does", "not", "exist"))

	cfg, err := qrdbdriver.ParseDSN("node://memory?database=noscratch")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	c := New(cfg)
	defer func() { _ = c.Close() }()

	_, err = c.Connect(context.Background())
	if err == nil {
		t.Fatal("expected Connect to fail when the scratch directory cannot be created")
	}
	var dirErr *DataDirError
	if !errors.As(err, &dirErr) {
		t.Fatalf("error = %v (%T), want *DataDirError", err, err)
	}
}

// ---------------------------------------------------------------------------
// Configuration projection
// ---------------------------------------------------------------------------

// TestNodeConfig_DisablesEveryProtocol verifies the node the connector starts
// binds no port at all. A zero ProtocolsConfig is the assertion: a protocol
// added upstream must default off here too.
func TestNodeConfig_DisablesEveryProtocol(t *testing.T) {
	cfg, err := qrdbdriver.ParseDSN("node://memory?database=proto")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}

	got := nodeConfig(cfg, "/data")
	if got.Protocols != (qrdbconfig.ProtocolsConfig{}) {
		t.Errorf("Protocols = %+v, want every protocol disabled", got.Protocols)
	}
}

// TestNodeConfig_ProjectsDSN verifies the data directory, database name,
// backend, and engine all come from the DSN.
func TestNodeConfig_ProjectsDSN(t *testing.T) {
	cfg, err := qrdbdriver.ParseDSN("node:///srv/data?backend=pebble&database=projected&engine=kv")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}

	got := nodeConfig(cfg, cfg.DataDir)
	if got.Raft.DataDir != "/srv/data" {
		t.Errorf("Raft.DataDir = %q, want %q", got.Raft.DataDir, "/srv/data")
	}
	if len(got.Databases) != 1 {
		t.Fatalf("Databases = %+v, want exactly one entry", got.Databases)
	}
	db := got.Databases[0]
	if db.Name != "projected" || db.Backend != "pebble" || db.Engine != "kv" {
		t.Errorf("Databases[0] = %+v, want {projected pebble kv}", db)
	}
}

// TestNodeConfig_ScratchDirOverridesDefault verifies the resolved directory is
// used rather than qrdbconfig.DefaultConfig's "./data", which would write into
// the caller's working directory.
func TestNodeConfig_ScratchDirOverridesDefault(t *testing.T) {
	cfg, err := qrdbdriver.ParseDSN("node://memory?database=scratchcfg")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}

	got := nodeConfig(cfg, "/tmp/scratch-xyz")
	if got.Raft.DataDir != "/tmp/scratch-xyz" {
		t.Errorf("Raft.DataDir = %q, want the resolved scratch directory", got.Raft.DataDir)
	}
	if got.Raft.DataDir == qrdbconfig.DefaultConfig().Raft.DataDir {
		t.Error("Raft.DataDir must not be left at the package default")
	}
}
