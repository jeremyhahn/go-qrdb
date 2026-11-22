// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// moveFixture builds a source tree with nested directories, an empty
// directory, an empty file, a file over 1 MiB and a 0600 file.
func moveFixture(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	big := bytes.Repeat([]byte("0123456789abcdef"), (1<<20)/16+4097)
	files := []struct {
		rel  string
		data []byte
		mode fs.FileMode
	}{
		{"a/b/c/deep.dat", []byte("deep"), 0o644},
		{"a/top.dat", []byte("top"), 0o640},
		{"a-z.dat", []byte("sorts before a/"), 0o644},
		{"empty.dat", nil, 0o644},
		{"big.dat", big, 0o644},
		{"secret.dat", []byte("secret"), 0o600},
	}
	if err := os.MkdirAll(filepath.Join(src, "a", "b", "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "emptydir"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		p := filepath.Join(src, filepath.FromSlash(f.rel))
		if err := os.WriteFile(p, f.data, f.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, f.mode); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

// snapshot records every entry's mode and content below root.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		v := info.Mode().String()
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			v += ":" + string(b)
		}
		out[rel] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func copyFixture(t *testing.T) (src, dst string, m *Manifest) {
	t.Helper()
	src = moveFixture(t)
	dst = filepath.Join(t.TempDir(), "dst")
	m, err := CopyTree(context.Background(), src, dst)
	if err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	return src, dst, m
}

func TestCopyTreeRoundTrip(t *testing.T) {
	src := moveFixture(t)
	before := snapshot(t, src)
	dst := filepath.Join(t.TempDir(), "dst")
	ctx := context.Background()
	m, err := CopyTree(ctx, src, dst)
	if err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	fromSrc, err := BuildManifest(ctx, src)
	if err != nil {
		t.Fatalf("BuildManifest(src): %v", err)
	}
	fromDst, err := BuildManifest(ctx, dst)
	if err != nil {
		t.Fatalf("BuildManifest(dst): %v", err)
	}
	if !reflect.DeepEqual(m, fromSrc) {
		t.Errorf("copy manifest != BuildManifest(src)\n got %+v\nwant %+v", m, fromSrc)
	}
	if !reflect.DeepEqual(m, fromDst) {
		t.Errorf("copy manifest != BuildManifest(dst)")
	}
	if err := VerifyCopy(ctx, dst, m); err != nil {
		t.Errorf("VerifyCopy: %v", err)
	}
	for i := 1; i < len(m.Entries); i++ {
		if m.Entries[i-1].Path >= m.Entries[i].Path {
			t.Errorf("manifest not sorted at %q, %q", m.Entries[i-1].Path, m.Entries[i].Path)
		}
	}
	paths := map[string]ManifestEntry{}
	for _, e := range m.Entries {
		if e.Path == "" || e.Path == ".." || strings.HasPrefix(e.Path, "/") || strings.Contains(e.Path, `\`) {
			t.Errorf("bad manifest path %q", e.Path)
		}
		paths[e.Path] = e
	}
	for _, want := range []string{"a", "a/b", "a/b/c", "a/b/c/deep.dat", "a/top.dat", "a-z.dat", "big.dat", "empty.dat", "emptydir", "secret.dat"} {
		if _, ok := paths[want]; !ok {
			t.Errorf("manifest lacks %q", want)
		}
	}
	if e := paths["secret.dat"]; e.Mode != 0o600 {
		t.Errorf("secret.dat mode = %v, want 0600", e.Mode)
	}
	if e := paths["emptydir"]; !e.Dir || e.Size != 0 || e.SHA256 != [32]byte{} || e.Mode != 0o750 {
		t.Errorf("emptydir entry = %+v", e)
	}
	if e := paths["big.dat"]; e.Size <= 1<<20 {
		t.Errorf("big.dat size = %d, want > 1 MiB", e.Size)
	}
	if e := paths["empty.dat"]; e.Size != 0 || e.SHA256 == [32]byte{} {
		t.Errorf("empty.dat entry = %+v (want size 0, SHA-256 of the empty string)", e)
	}
	got := snapshot(t, dst)
	delete(got, ".")
	want := snapshot(t, src)
	delete(want, ".")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("destination tree differs from source")
	}
	if info, err := os.Stat(dst); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("dst mode = %v, %v; want 0700", info, err)
	}
	if after := snapshot(t, src); !reflect.DeepEqual(before, after) {
		t.Errorf("source changed by CopyTree")
	}
}

func TestCopyTreeRefusals(t *testing.T) {
	base := t.TempDir()
	src := moveFixture(t)
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(base, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyExisting := filepath.Join(base, "empty-existing")
	if err := os.Mkdir(emptyExisting, 0o700); err != nil {
		t.Fatal(err)
	}
	outer := filepath.Join(base, "outer")
	if err := os.MkdirAll(filepath.Join(outer, "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	linkIn := filepath.Join(base, "link-in")
	if err := os.MkdirAll(linkIn, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linkIn, "target"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(linkIn, "link")); err != nil {
		t.Fatal(err)
	}
	linkOut := filepath.Join(base, "link-out")
	if err := os.MkdirAll(linkOut, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, filepath.Join(linkOut, "link")); err != nil {
		t.Fatal(err)
	}
	fresh := func(name string) string { return filepath.Join(t.TempDir(), name) }

	cases := []struct {
		name     string
		src, dst string
		pipe     bool
	}{
		{"relative src", "rel/src", fresh("d"), false},
		{"unclean src", src + "/../src", fresh("d"), false},
		{"relative dst", src, "rel/dst", false},
		{"unclean dst", src, base + "//dst", false},
		{"missing src", filepath.Join(base, "nope"), fresh("d"), false},
		{"src is a file", file, fresh("d"), false},
		{"dst exists", src, existing, false},
		{"dst exists empty", src, emptyExisting, false},
		{"dst parent missing", src, filepath.Join(base, "no-parent", "dst"), false},
		{"dst inside src", src, filepath.Join(src, "a", "dst"), false},
		{"src inside dst", filepath.Join(outer, "inner"), outer, false},
		{"src equals dst", src, src, false},
		{"symlink inside tree", linkIn, fresh("d"), false},
		{"symlink outside tree", linkOut, fresh("d"), false},
		{"named pipe", "", fresh("d"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.src
			if tc.pipe {
				if runtime.GOOS == "windows" {
					t.Skip("named pipes need unix")
				}
				s = filepath.Join(t.TempDir(), "pipe-src")
				if err := os.Mkdir(s, 0o700); err != nil {
					t.Fatal(err)
				}
				// mkfifo(1) keeps the test portable to build on every GOOS.
				if out, err := exec.Command("mkfifo", filepath.Join(s, "fifo")).CombinedOutput(); err != nil {
					t.Skipf("mkfifo: %v: %s", err, out)
				}
			}
			_, err := CopyTree(context.Background(), s, tc.dst)
			var se *MoveStepError
			if !errors.As(err, &se) {
				t.Fatalf("CopyTree err = %v, want *MoveStepError", err)
			}
			if se.Phase != "copy" {
				t.Errorf("Phase = %q, want copy", se.Phase)
			}
			if se.NodeID != "" {
				t.Errorf("NodeID = %q, want empty", se.NodeID)
			}
			assertNoAbsPath(t, err, base, s, tc.dst)
		})
	}
}

func assertNoAbsPath(t *testing.T, err error, paths ...string) {
	t.Helper()
	msg := err.Error()
	for _, p := range paths {
		if filepath.IsAbs(p) && strings.Contains(msg, p) {
			t.Errorf("error message %q carries absolute path %q", msg, p)
		}
	}
	if strings.Contains(msg, os.TempDir()) {
		t.Errorf("error message %q carries the temp dir", msg)
	}
}

func TestCopyTreeCancelled(t *testing.T) {
	src := moveFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := CopyTree(ctx, src, filepath.Join(t.TempDir(), "dst"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyTree err = %v, want context.Canceled", err)
	}
	var se *MoveStepError
	if !errors.As(err, &se) || se.Phase != "copy" {
		t.Errorf("err = %v, want *MoveStepError phase copy", err)
	}
	if _, err := BuildManifest(ctx, src); !errors.Is(err, context.Canceled) {
		t.Errorf("BuildManifest err = %v, want context.Canceled", err)
	}
}

func TestVerifyCopyMismatch(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		reason string
		mutate func(t *testing.T, dst string)
	}{
		{"truncated", "a/top.dat", "size", func(t *testing.T, dst string) {
			if err := os.Truncate(filepath.Join(dst, "a", "top.dat"), 1); err != nil {
				t.Fatal(err)
			}
		}},
		{"byte flipped", "a/top.dat", "digest", func(t *testing.T, dst string) {
			p := filepath.Join(dst, "a", "top.dat")
			if err := os.WriteFile(p, []byte("tOp"), 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{"added file", "a/b/new.dat", "extra", func(t *testing.T, dst string) {
			if err := os.WriteFile(filepath.Join(dst, "a", "b", "new.dat"), []byte("n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"removed file", "a/b/c/deep.dat", "missing", func(t *testing.T, dst string) {
			if err := os.Remove(filepath.Join(dst, "a", "b", "c", "deep.dat")); err != nil {
				t.Fatal(err)
			}
		}},
		{"file replaced by directory", "secret.dat", "type", func(t *testing.T, dst string) {
			p := filepath.Join(dst, "secret.dat")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"chmod", "a-z.dat", "mode", func(t *testing.T, dst string) {
			if err := os.Chmod(filepath.Join(dst, "a-z.dat"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, dst, m := copyFixture(t)
			tc.mutate(t, dst)
			err := VerifyCopy(context.Background(), dst, m)
			var se *MoveStepError
			if !errors.As(err, &se) || se.Phase != "verify" {
				t.Fatalf("VerifyCopy err = %v, want *MoveStepError phase verify", err)
			}
			var mm *ManifestMismatchError
			if !errors.As(err, &mm) {
				t.Fatalf("VerifyCopy err = %v, want *ManifestMismatchError", err)
			}
			if mm.Path != tc.path || mm.Reason != tc.reason {
				t.Errorf("mismatch = {%q %q}, want {%q %q}", mm.Path, mm.Reason, tc.path, tc.reason)
			}
			if se.Path != tc.path {
				t.Errorf("MoveStepError.Path = %q, want %q", se.Path, tc.path)
			}
			assertNoAbsPath(t, err, src, dst)
		})
	}
}

func TestMoveStepErrorMessage(t *testing.T) {
	cause := &ManifestMismatchError{Path: "a/b", Reason: "digest"}
	err := &MoveStepError{NodeID: "n1", Phase: "verify", Path: "a/b", Err: cause}
	msg := err.Error()
	for _, want := range []string{`"verify"`, `"n1"`, `"a/b"`, "digest"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if !errors.Is(err, cause) {
		t.Errorf("Unwrap does not reach the cause")
	}
	if err := VerifyCopy(context.Background(), t.TempDir(), nil); err == nil {
		t.Errorf("VerifyCopy(nil manifest) = nil, want error")
	}
}
