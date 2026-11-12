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

//go:build clusterproof

package soak

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
)

// cleanBundle writes the bundle of a proven run once per test.
func cleanBundle(t *testing.T) (dir string, checkers []history.Checker) {
	t.Helper()
	o := testOptions(t, newFakeKV(), &fakeNemesis{})
	if v := run(t, o); v.Exit != ExitProven {
		t.Fatalf("clean run: %s exit %d, reasons %q", v.Outcome, v.Exit, v.Reasons)
	}
	return o.Bundle, o.Checkers
}

// copyBundle copies src to a fresh directory for one tampering.
func copyBundle(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "copy")
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("CopyFS: %v", err)
	}
	return dst
}

// rewrite replaces a member and, when reseal is set, rewrites its manifest
// digest too: a tamperer who knows the format.
func rewrite(t *testing.T, dir, rel string, edit func([]byte) []byte, reseal bool) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	data = edit(data)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !reseal {
		return
	}
	var m Manifest
	if err := decodeMember(dir, fileManifest, &m); err != nil {
		t.Fatal(err)
	}
	m.Files[rel] = digest(data)
	enc, err := encodeJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileManifest), enc, 0o644); err != nil {
		t.Fatal(err)
	}
}

// editJSON decodes a member into v, edits it and encodes it back.
func editJSON[T any](t *testing.T, edit func(*T)) func([]byte) []byte {
	return func(data []byte) []byte {
		var v T
		if err := decodeStrict(data, &v); err != nil {
			t.Fatal(err)
		}
		edit(&v)
		out, err := encodeJSON(v)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
}

func TestCheckDetectsTampering(t *testing.T) {
	src, checkers := cleanBundle(t)
	cases := map[string]struct {
		tamper func(t *testing.T, dir string)
		reason string
	}{
		"edited history": {
			func(t *testing.T, dir string) {
				rewrite(t, dir, fileHistory, func(b []byte) []byte { return bytes.Replace(b, []byte("\n"), []byte(" \n"), 1) }, false)
			},
			"history.jsonl does not match its manifest checksum",
		},
		"edited node file": {
			func(t *testing.T, dir string) {
				rewrite(t, dir, nodeFile("n2"), func(b []byte) []byte { return append(b, ' ') }, false)
			},
			"nodes/n2/node.json does not match its manifest checksum",
		},
		"added file": {
			func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			"notes.txt is not in the manifest",
		},
		"removed file": {
			func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, fileTimings)); err != nil {
					t.Fatal(err)
				}
			},
			"timings.json is in the manifest and missing from the bundle",
		},
		"symlinked file": {
			func(t *testing.T, dir string) {
				if err := os.Symlink(fileRun, filepath.Join(dir, "link.json")); err != nil {
					t.Fatal(err)
				}
			},
			"link.json is not a regular file",
		},
		"resealed evidence": {
			func(t *testing.T, dir string) {
				rewrite(t, dir, fileRun, editJSON(t, func(r *RunRecord) { r.Liveness.Detail = "altered" }), true)
			},
			"verdict.json does not follow from the evidence",
		},
		"resealed seal type": {
			func(t *testing.T, dir string) {
				rewrite(t, dir, fileRun, editJSON(t, func(r *RunRecord) { r.Seal = "none" }), true)
			},
			"verdict.json does not follow from the evidence",
		},
		"resealed verdict": {
			func(t *testing.T, dir string) {
				rewrite(t, dir, fileVerdict, editJSON(t, func(v *Verdict) { v.Reasons = []string{"nothing"} }), true)
			},
			"verdict.json does not follow from the evidence",
		},
		"resealed timeline": {
			func(t *testing.T, dir string) {
				rewrite(t, dir, fileTimeline, func(b []byte) []byte { return b[:bytes.IndexByte(b, '\n')+1] }, true)
			},
			"timeline.jsonl does not follow from the evidence",
		},
		"manifest drops a member": {
			func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, fileTimings)); err != nil {
					t.Fatal(err)
				}
				rewrite(t, dir, fileManifest, editJSON(t, func(m *Manifest) { delete(m.Files, fileTimings) }), false)
			},
			"manifest lacks timings.json",
		},
		"manifest escapes the bundle": {
			func(t *testing.T, dir string) {
				rewrite(t, dir, fileManifest, editJSON(t, func(m *Manifest) { m.Files["../outside"] = digest(nil) }), false)
			},
			`invalid member "../outside"`,
		},
		"unknown manifest format": {
			func(t *testing.T, dir string) {
				rewrite(t, dir, fileManifest, editJSON(t, func(m *Manifest) { m.Format = 99 }), false)
			},
			"unsupported bundle format 99",
		},
		"missing manifest": {
			func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, fileManifest)); err != nil {
					t.Fatal(err)
				}
			},
			"cannot read manifest.json",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := copyBundle(t, src)
			c.tamper(t, dir)
			_, err := Check(dir, CheckOptions{Checkers: checkers})
			be, ok := errors.AsType[*BundleError](err)
			if !ok {
				t.Fatalf("Check of a tampered bundle = %v, want a *BundleError", err)
			}
			if !strings.Contains(be.Error(), c.reason) {
				t.Fatalf("Check error %q does not say %q", be.Error(), c.reason)
			}
		})
	}
}

func TestCheckNeedsTheRunsCheckers(t *testing.T) {
	src, _ := cleanBundle(t)
	_, err := Check(src, CheckOptions{})
	if _, ok := errors.AsType[*BundleError](err); !ok || !strings.Contains(err.Error(), "other checkers than the run did") {
		t.Fatalf("Check with other checkers = %v", err)
	}
}

func TestCheckMain(t *testing.T) {
	src, checkers := cleanBundle(t)
	tampered := copyBundle(t, src)
	rewrite(t, tampered, fileNemesis, func(b []byte) []byte { return append(b, '\n') }, false)
	configure := func(o *Options) error { o.Checkers = checkers; return nil }

	cases := []struct {
		name   string
		bundle string
		want   int
		out    string
	}{
		{"verified", src, ExitProven, "manifest sha256:"},
		{"tampered", tampered, ExitInvalid, "bundle invalid"},
		{"absent", filepath.Join(t.TempDir(), "none"), ExitInvalid, "bundle invalid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			env := Env{
				Getenv:    func(k string) string { return map[string]string{EnvBundle: c.bundle}[k] },
				Stdout:    &stdout,
				Stderr:    &stderr,
				Configure: configure,
			}
			if got := CheckMain(context.Background(), env); got != c.want {
				t.Fatalf("CheckMain = %d, want %d; stdout %q stderr %q", got, c.want, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String()+stderr.String(), c.out) {
				t.Fatalf("output lacks %q: stdout %q stderr %q", c.out, stdout.String(), stderr.String())
			}
		})
	}
}

func TestLatestBundle(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"2026-10-12T09-00-00Z", "2026-10-12T10-00-00Z", "2026-10-11T23-59-59Z"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "zz-not-a-bundle"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := latestBundle(root)
	if err != nil || got != filepath.Join(root, "2026-10-12T10-00-00Z") {
		t.Fatalf("latestBundle = %q, %v", got, err)
	}
	if _, err := latestBundle(t.TempDir()); err == nil {
		t.Fatal("latestBundle found a bundle in an empty root")
	}
	if _, err := latestBundle(filepath.Join(root, "absent")); err == nil {
		t.Fatal("latestBundle read an absent root")
	}
}

func TestBundleRefusesToOverwrite(t *testing.T) {
	o := testOptions(t, newFakeKV(), &fakeNemesis{})
	if err := os.MkdirAll(o.Bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), o); err == nil {
		t.Fatal("Run wrote into an existing bundle directory")
	}
}

func TestValidMember(t *testing.T) {
	for rel, want := range map[string]bool{
		"run.json": true, "nodes/n1/node.json": true,
		"": false, ".": false, "..": false, "/etc/passwd": false, "a//b": false, "a/./b": false, `a\b`: false, "a/../b": false,
	} {
		if got := validMember(rel); got != want {
			t.Errorf("validMember(%q) = %v, want %v", rel, got, want)
		}
	}
}
