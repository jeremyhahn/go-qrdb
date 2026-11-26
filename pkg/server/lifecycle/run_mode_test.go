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

package lifecycle

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
)

func readRunModeMarker(t *testing.T, dir string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, runModeMarker)) // #nosec G304 -- test temp dir
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	return string(data), true
}

func TestClaimRunMode_FreshDirWritesMarker(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{capabilities.ModeStandalone, "standalone\n"},
		{capabilities.ModeRaft, "raft\n"},
		{"", "raft\n"},
	}
	for _, tc := range cases {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			if err := ClaimRunMode(dir, tc.mode); err != nil {
				t.Fatalf("ClaimRunMode: %v", err)
			}
			got, ok := readRunModeMarker(t, dir)
			if !ok || got != tc.want {
				t.Fatalf("marker = %q (present %v), want %q", got, ok, tc.want)
			}
			if err := ClaimRunMode(dir, tc.mode); err != nil {
				t.Fatalf("same-mode restart: %v", err)
			}
		})
	}
}

func TestClaimRunMode_MismatchRefused(t *testing.T) {
	cases := []struct {
		recorded, configured, wantRecorded, wantConfigured string
	}{
		{capabilities.ModeStandalone, capabilities.ModeRaft, "standalone", "raft"},
		{capabilities.ModeRaft, capabilities.ModeStandalone, "raft", "standalone"},
		{capabilities.ModeStandalone, "", "standalone", "raft"},
		{"", capabilities.ModeStandalone, "raft", "standalone"},
	}
	for _, tc := range cases {
		t.Run(tc.recorded+"->"+tc.configured, func(t *testing.T) {
			dir := t.TempDir()
			if err := ClaimRunMode(dir, tc.recorded); err != nil {
				t.Fatalf("first claim: %v", err)
			}
			err := ClaimRunMode(dir, tc.configured)
			var mm *RunModeMismatchError
			if !errors.As(err, &mm) {
				t.Fatalf("err = %v, want *RunModeMismatchError", err)
			}
			if mm.Recorded != tc.wantRecorded || mm.Configured != tc.wantConfigured || mm.DataDir != dir {
				t.Fatalf("got %+v", mm)
			}
			msg := err.Error()
			for _, s := range []string{"mode: standalone", "mode: raft", "fresh data directory", "was initialized in"} {
				if !strings.Contains(msg, s) {
					t.Fatalf("message %q lacks %q", msg, s)
				}
			}
			got, _ := readRunModeMarker(t, dir)
			if got != tc.wantRecorded+"\n" {
				t.Fatalf("marker changed to %q", got)
			}
		})
	}
}

func TestClaimRunMode_UnknownMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, runModeMarker), []byte("bogus\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ClaimRunMode(dir, capabilities.ModeStandalone)
	var me *RunModeMarkerError
	if !errors.As(err, &me) {
		t.Fatalf("err = %v, want *RunModeMarkerError", err)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("message %q does not name the content", err.Error())
	}
}

func TestClaimRunMode_LeftoverTmpIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, runModeMarker+".tmp"), []byte("standalone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ClaimRunMode(dir, capabilities.ModeRaft); err != nil {
		t.Fatalf("ClaimRunMode: %v", err)
	}
	if got, _ := readRunModeMarker(t, dir); got != "raft\n" {
		t.Fatalf("marker = %q", got)
	}
}

// TestClaimRunMode_MarkerIOFailures pins the guard failing closed when the
// marker cannot be read or written: each failure is a *RunModeMarkerError
// naming the path and wrapping the OS error, and no marker is left behind.
func TestClaimRunMode_MarkerIOFailures(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T) (dataDir string)
		wantPath func(dataDir string) string
		wantErr  error
		needPerm bool
	}{
		{
			name: "marker is a directory",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.Mkdir(filepath.Join(dir, runModeMarker), 0o700); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			wantPath: func(d string) string { return filepath.Join(d, runModeMarker) },
		},
		{
			name: "data dir under a regular file",
			setup: func(t *testing.T) string {
				file := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(file, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(file, "data")
			},
			wantPath: func(d string) string { return filepath.Join(d, runModeMarker) },
		},
		{
			name: "data dir cannot be created",
			setup: func(t *testing.T) string {
				return filepath.Join(readOnlyDir(t), "data")
			},
			wantPath: func(d string) string { return d },
			wantErr:  fs.ErrPermission,
			needPerm: true,
		},
		{
			name:     "marker cannot be written",
			setup:    readOnlyDir,
			wantPath: func(d string) string { return filepath.Join(d, runModeMarker) + ".tmp" },
			wantErr:  fs.ErrPermission,
			needPerm: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needPerm && os.Geteuid() == 0 {
				t.Skip("root ignores directory permissions")
			}
			dir := tc.setup(t)
			err := ClaimRunMode(dir, capabilities.ModeStandalone)
			var me *RunModeMarkerError
			if !errors.As(err, &me) {
				t.Fatalf("err = %v, want *RunModeMarkerError", err)
			}
			if me.Err == nil || errors.Unwrap(err) != me.Err {
				t.Fatalf("err %v does not wrap the OS error", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want errors.Is %v", err, tc.wantErr)
			}
			if want := tc.wantPath(dir); me.Path != want {
				t.Fatalf("Path = %q, want %q", me.Path, want)
			}
			if msg := err.Error(); !strings.Contains(msg, me.Path) || !strings.Contains(msg, me.Err.Error()) {
				t.Fatalf("message %q lacks path or cause", msg)
			}
			if _, statErr := os.Stat(filepath.Join(dir, runModeMarker)); tc.name != "marker is a directory" && statErr == nil {
				t.Fatal("a marker was written despite the failure")
			}
		})
	}
}

// readOnlyDir is an existing directory nobody but root may create entries in.
func readOnlyDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir
}

// TestLifecycleErrorMessages pins the message and the wrapped cause of the
// lifecycle error types no boot path in this package's tests reaches.
func TestLifecycleErrorMessages(t *testing.T) {
	cause := errors.New("cause")
	cases := []struct {
		name    string
		err     error
		want    string
		wrapped bool
	}{
		{"barrier service", &BarrierServiceError{Err: cause}, "cause", true},
		{"database catalog", &DatabaseCatalogError{Err: cause}, "database catalog", true},
		{"group service", &GroupServiceError{Err: cause}, "cause", true},
		{"rag chunk defaults", &RAGChunkDefaultsError{Err: cause}, "rag chunking defaults", true},
		{"objstore barrier sealed", &ObjstoreBarrierSealedError{}, "storage barrier is sealed", false},
		{"node identity with cause", &NodeIdentityError{Reason: "bad pair", Err: cause}, "node identity: bad pair: cause", true},
		{"node identity", &NodeIdentityError{Reason: "no node certificate"}, "node identity: no node certificate", false},
		{"run-mode marker unknown", &RunModeMarkerError{Path: "p", Content: "x"}, `records unknown mode "x"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if msg := tc.err.Error(); !strings.Contains(msg, tc.want) || !strings.HasPrefix(msg, "lifecycle: ") {
				t.Fatalf("message %q lacks %q", msg, tc.want)
			}
			if got := errors.Is(tc.err, cause); got != tc.wrapped {
				t.Fatalf("errors.Is(cause) = %v, want %v", got, tc.wrapped)
			}
		})
	}
	if (&NodeIdentityError{Reason: "r"}).Unwrap() != nil {
		t.Fatal("NodeIdentityError without a cause unwraps to non-nil")
	}
	if (&RunModeMarkerError{Path: "p"}).Unwrap() != nil {
		t.Fatal("RunModeMarkerError without a cause unwraps to non-nil")
	}
}

// TestNodeSOPINsUnattached pins that a node with no tenant onboarding has no
// SO PIN seam and ignores an attach.
func TestNodeSOPINsUnattached(t *testing.T) {
	n := &Node{}
	n.AttachSOPINs(nil)
	if n.SOPINs() != nil {
		t.Fatal("SOPINs() on a node without onboarding is non-nil")
	}
}

// TestKVSealBackendCloseNoop pins that closing the seal backend leaves the
// lifecycle-owned KV store alone and reports no error.
func TestKVSealBackendCloseNoop(t *testing.T) {
	if err := (kvSealBackend{}).Close(); err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}
}
