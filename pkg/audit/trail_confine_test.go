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

package audit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// outsideDir is a directory outside the trail's, holding one empty file a
// link is pointed at, and the check that the trail never reached it.
func outsideDir(t *testing.T) (dir, victim string, untouched func()) {
	t.Helper()
	dir = t.TempDir()
	victim = filepath.Join(dir, "victim.ndjson")
	if err := os.WriteFile(victim, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, victim, func() {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(victim); len(entries) != 1 || err != nil || info.Size() != 0 {
			t.Fatalf("the trail reached outside its directory: %v, %v, %v", entries, info, err)
		}
	}
}

// confineEvent is one record to emit.
var confineEvent = AuditEvent{Actor: "a", Resource: "r", Action: "x", Outcome: OutcomeSuccess}

// OpenTrailIn confines the trail to its directory: a symbolic link at the
// trail's name, or at a directory on its path, that resolves outside refuses
// the trail, the file it points at is not written, and a name that leaves
// the directory is refused before anything is opened.
func TestOpenTrailIn_RefusesALinkOutOfTheDirectory(t *testing.T) {
	outside, victim, untouched := outsideDir(t)
	dir := t.TempDir()
	if err := os.Symlink(victim, filepath.Join(dir, DefaultTrailFile)); err != nil {
		t.Fatal(err)
	}
	if trail, err := OpenTrailIn(dir, DefaultTrailFile); err == nil {
		_ = trail.Close()
		t.Fatal("a trail linked out of its directory was opened")
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	if trail, err := OpenTrailIn(dir, filepath.Join("linked", "trail.ndjson")); err == nil {
		_ = trail.Close()
		t.Fatal("a trail below a directory linked out of its directory was opened")
	}
	untouched()

	var fErr *FileError
	if _, err := OpenTrailIn(dir, filepath.Join("..", "escape.ndjson")); !errors.As(err, &fErr) || !errors.Is(err, ErrOutsideTrailDir) {
		t.Fatalf("a name leaving the directory = %v, want FileError wrapping ErrOutsideTrailDir", err)
	}
}

// A link that stays inside the trail's directory is followed: the
// confinement refuses leaving the directory, not links as such.
func TestOpenTrailIn_FollowsALinkThatStaysInside(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("logs", filepath.Join(dir, "audit")); err != nil {
		t.Fatal(err)
	}
	trail, err := OpenTrailIn(dir, filepath.Join("audit", DefaultTrailFile))
	if err != nil {
		t.Fatalf("a link inside the directory: %v", err)
	}
	if err := trail.Emit(context.Background(), confineEvent); err != nil {
		t.Fatal(err)
	}
	if err := trail.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, "logs", DefaultTrailFile)); err != nil || info.Size() == 0 {
		t.Fatalf("the trail was not written through the inside link: %v", err)
	}
}

// A directory on the trail's path replaced by a link out of the trail's
// directory while the trail is open does not carry it there: the rotation's
// rename and the reopen after it resolve inside the directory, so they fail
// and nothing is created where the link points.
func TestChainFileLogger_RotationStaysInTheDirectory(t *testing.T) {
	outside, _, untouched := outsideDir(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "audit"), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := NewFileLoggerIn(dir, filepath.Join("audit", DefaultTrailFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	if err := l.Emit(context.Background(), confineEvent); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(filepath.Join(dir, "audit"), filepath.Join(dir, "audit-moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "audit")); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	rotErr := l.rotate()
	l.mu.Unlock()
	if rotErr == nil {
		t.Fatal("a rotation through a link out of the directory succeeded")
	}
	untouched()
}

// The readers stay in the directory too: a query and a verification of a
// trail whose file was replaced by a link out of the directory read nothing
// through it.
func TestChainFileLogger_ReadersStayInTheDirectory(t *testing.T) {
	_, victim, _ := outsideDir(t)
	if err := os.WriteFile(victim, []byte(`{"timestamp":"2026-01-01T00:00:00Z","actor":"outsider","resource":"r","action":"x","outcome":"success"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultTrailFile)
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}
	var fErr *FileError
	if page, err := l.Query(context.Background(), Filter{Actor: "outsider"}); !errors.As(err, &fErr) {
		t.Fatalf("a query of a trail linked out of the directory = %+v, %v, want FileError", page, err)
	}
	if v, err := l.VerifyChainSet(context.Background()); !errors.As(err, &fErr) {
		t.Fatalf("a verification of a trail linked out of the directory = %+v, %v, want FileError", v, err)
	}
}
