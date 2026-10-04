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

// go-qrdb#268: rotation's rename is a commit point, and a commit point is not
// one until the containing directory is durable.
package audit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRotate_DoesNotReportSuccessBeforeTheArchiveEntryIsDurable.
//
// rotate appends a chain-anchor entry closing the old chain, then renames the
// file to its archive name. If that directory entry is lost to a crash while
// the file's synced contents survive, the anchored file is still reachable at
// l.path -- and openFile appends the NEW chain onto it. The result is one file
// holding two chains with an anchor buried in the middle, which no verifier
// can split, because the anchor's claim is "the next FILE starts a new chain".
//
// Observed at the operation: the bytes are identical either way, so the sync
// is failed at its boundary and rotate must surface it.
func TestRotate_DoesNotReportSuccessBeforeTheArchiveEntryIsDurable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	l.Emit(context.Background(), testEvent("henry", "kv", "delete"))

	sentinel := errors.New("directory sync refused")
	l.dir.syncFunc = func(*os.File) error { return sentinel }

	l.mu.Lock()
	rotErr := l.rotate()
	l.mu.Unlock()

	if !errors.Is(rotErr, sentinel) {
		t.Fatalf("rotate = %v; want the directory-sync failure. Reporting the rotation "+
			"complete before the archive's directory entry is durable lets a crash leave "+
			"the anchored chain and its successor in one file", rotErr)
	}
	var fErr *FileError
	if !errors.As(rotErr, &fErr) {
		t.Errorf("error = %T; want it wrapped as *FileError, the type this package's "+
			"callers already match on", rotErr)
	}
}

// TestRotate_KeepsLoggingAfterADurabilityFailure.
//
// rotate closes the live file before renaming, so an early return on the
// rename or the sync would leave l.f nil and every later Emit silently
// dropping events -- an audit logger that stops auditing because a fsync
// blipped. The failure must be reported without disabling the log.
func TestRotate_KeepsLoggingAfterADurabilityFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	l.dir.syncFunc = func(*os.File) error { return errors.New("directory sync refused") }
	l.mu.Lock()
	_ = l.rotate()
	l.mu.Unlock()
	l.dir.syncFunc = nil

	if l.f == nil {
		t.Fatal("logger has no open file after a failed rotation; every subsequent Emit " +
			"would drop its event")
	}
	l.Emit(context.Background(), testEvent("henry", "kv", "create"))

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read active file: %v", err)
	}
	if len(data) == 0 {
		t.Error("the event emitted after a failed rotation was not written")
	}
}

// TestRotate_SyncsTheDirectoryOnASuccessfulRotation records which directory is
// made durable: the one holding both the archive's new name and the old name's
// removal. Source and destination share a parent, so it is synced once.
func TestRotate_SyncsTheDirectoryOnASuccessfulRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	// The directory is synced through the trail's root: the file synced is
	// the trail's directory itself.
	var synced []os.FileInfo
	l.dir.syncFunc = func(f *os.File) error {
		info, err := f.Stat()
		if err != nil {
			return err
		}
		synced = append(synced, info)
		return f.Sync()
	}
	l.mu.Lock()
	rotErr := l.rotate()
	l.mu.Unlock()
	l.dir.syncFunc = nil

	if rotErr != nil {
		t.Fatalf("rotate: %v", rotErr)
	}
	want, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(synced) != 1 || !os.SameFile(synced[0], want) {
		t.Errorf("synced %d directories; want exactly the trail's directory %s, once", len(synced), dir)
	}
}
