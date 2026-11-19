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

package timeseries_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	pkgts "github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	sdkts "github.com/jeremyhahn/go-qrdb/sdk/go/timeseries"
)

// testEncryptor returns an at-rest encryptor over a passphrase-sealed,
// initialized (and therefore unsealed) barrier on in-memory seal storage.
func testEncryptor(t *testing.T) *atrest.Encryptor {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	barrier, err := seal.NewStorageBarrier(logger, seal.NewStorageAdapter(memory.NewStorage()),
		seal.BarrierConfig{RootKeyPath: "root-key"}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	creds := seal.MustCredentials(context.Background(), []byte("sdk-timeseries-test-passphrase"))
	if err := barrier.Initialize(context.Background(), creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return atrest.New(barrier)
}

// TestNewTimeSeriesStorageWithDir_EmptyDir checks the empty directory is
// refused with the typed error before anything is opened.
func TestNewTimeSeriesStorageWithDir_EmptyDir(t *testing.T) {
	ts, err := sdkts.NewTimeSeriesStorageWithDir("")
	var empty *pkgts.EmptyDirError
	if !errors.As(err, &empty) || ts != nil {
		t.Fatalf("NewTimeSeriesStorageWithDir(\"\") = %v, %v; want nil, *EmptyDirError", ts, err)
	}
}

// TestNewTimeSeriesStorageWithEncryptor_EmptyDir checks an encryptor does not
// bypass the empty-directory check.
func TestNewTimeSeriesStorageWithEncryptor_EmptyDir(t *testing.T) {
	ts, err := sdkts.NewTimeSeriesStorageWithEncryptor("", testEncryptor(t))
	var empty *pkgts.EmptyDirError
	if !errors.As(err, &empty) || ts != nil {
		t.Fatalf("NewTimeSeriesStorageWithEncryptor(\"\") = %v, %v; want nil, *EmptyDirError", ts, err)
	}
}

// TestNewTimeSeriesStorageWithEncryptor_UnopenableDir checks a directory
// Pebble cannot open, a path under a regular file, reports the typed error
// naming that directory.
func TestNewTimeSeriesStorageWithEncryptor_UnopenableDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(file, "ts")
	ts, err := sdkts.NewTimeSeriesStorageWithEncryptor(dir, nil)
	var openErr *pkgts.OpenDirError
	if !errors.As(err, &openErr) || ts != nil {
		t.Fatalf("NewTimeSeriesStorageWithEncryptor(%q) = %v, %v; want nil, *OpenDirError", dir, ts, err)
	}
	if openErr.Dir != dir || openErr.Unwrap() == nil {
		t.Fatalf("OpenDirError = {Dir:%q Err:%v}, want Dir %q and a cause", openErr.Dir, openErr.Unwrap(), dir)
	}
}

// TestTimeSeriesDirStorage_BatchCommitsToOwnedDatabase checks Batch returns
// a live batch over the owned database, and that a closed store's database
// refuses one.
func TestTimeSeriesDirStorage_BatchCommitsToOwnedDatabase(t *testing.T) {
	ts, err := sdkts.NewTimeSeriesStorageWithEncryptor(t.TempDir(), testEncryptor(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	b := ts.Batch()
	if err := b.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("batch Set: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("batch Commit: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("batch Close: %v", err)
	}
	if err := ts.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed := ts.Batch()
	if err := closed.Set([]byte("k"), []byte("v")); err == nil {
		t.Fatal("a batch over a closed database accepted a write")
	}
}
