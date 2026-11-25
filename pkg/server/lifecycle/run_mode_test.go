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

func mkdirs(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(dir, n), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", n, err)
		}
	}
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
			if mm.Recorded != tc.wantRecorded || mm.Configured != tc.wantConfigured || mm.Inferred || mm.DataDir != dir {
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

func TestClaimRunMode_LegacyDirInferred(t *testing.T) {
	cases := []struct {
		name  string
		dirs  []string
		owner string
		other string
	}{
		{"pebble", []string{"pebble", "seal"}, capabilities.ModeStandalone, capabilities.ModeRaft},
		{"file", []string{"file"}, capabilities.ModeStandalone, capabilities.ModeRaft},
		{"objstore", []string{"objstore"}, capabilities.ModeStandalone, capabilities.ModeRaft},
		{"wal+nodehost", []string{"wal", "nodehost", "databases"}, capabilities.ModeRaft, capabilities.ModeStandalone},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/same", func(t *testing.T) {
			dir := t.TempDir()
			mkdirs(t, dir, tc.dirs...)
			if err := ClaimRunMode(dir, tc.owner); err != nil {
				t.Fatalf("ClaimRunMode: %v", err)
			}
			if got, ok := readRunModeMarker(t, dir); !ok || got != tc.owner+"\n" {
				t.Fatalf("marker = %q (present %v)", got, ok)
			}
		})
		t.Run(tc.name+"/other", func(t *testing.T) {
			dir := t.TempDir()
			mkdirs(t, dir, tc.dirs...)
			err := ClaimRunMode(dir, tc.other)
			var mm *RunModeMismatchError
			if !errors.As(err, &mm) {
				t.Fatalf("err = %v, want *RunModeMismatchError", err)
			}
			if !mm.Inferred || mm.Recorded != tc.owner || mm.Configured != tc.other {
				t.Fatalf("got %+v", mm)
			}
			msg := err.Error()
			for _, s := range []string{"mode: " + tc.owner, "mode: " + tc.other, "fresh data directory", "holds " + tc.owner + " stores"} {
				if !strings.Contains(msg, s) {
					t.Fatalf("message %q lacks %q", msg, s)
				}
			}
			if _, ok := readRunModeMarker(t, dir); ok {
				t.Fatal("marker written on refusal")
			}
		})
	}
}

func TestClaimRunMode_AmbiguousRefused(t *testing.T) {
	for _, mode := range []string{capabilities.ModeStandalone, capabilities.ModeRaft} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			mkdirs(t, dir, "pebble", "wal")
			err := ClaimRunMode(dir, mode)
			var amb *RunModeAmbiguousError
			if !errors.As(err, &amb) {
				t.Fatalf("err = %v, want *RunModeAmbiguousError", err)
			}
			if len(amb.Standalone) != 1 || amb.Standalone[0] != "pebble" || len(amb.Raft) != 1 || amb.Raft[0] != "wal" {
				t.Fatalf("got %+v", amb)
			}
			msg := err.Error()
			for _, s := range []string{"pebble", "wal", "fresh data directory"} {
				if !strings.Contains(msg, s) {
					t.Fatalf("message %q lacks %q", msg, s)
				}
			}
			if _, ok := readRunModeMarker(t, dir); ok {
				t.Fatal("marker written on refusal")
			}
		})
	}
}

func TestClaimRunMode_SharedOnlyIsFresh(t *testing.T) {
	dir := t.TempDir()
	mkdirs(t, dir, "seal", "databases")
	if err := os.WriteFile(filepath.Join(dir, "pebble"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ClaimRunMode(dir, capabilities.ModeRaft); err != nil {
		t.Fatalf("ClaimRunMode: %v", err)
	}
	if got, _ := readRunModeMarker(t, dir); got != "raft\n" {
		t.Fatalf("marker = %q", got)
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
