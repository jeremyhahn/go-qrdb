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
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A package's coverage depends on which of its files the build tags compile:
// measured on the same tree, pkg/api/grpc reads 90.7% under the default
// feature set and 77.1% under `codec_json raft` alone. A baseline is only a
// baseline for the tag set it was measured under, so the tool records that set
// and refuses to compare a profile measured under another one.

// TestRun_CheckRefusesAProfileMeasuredUnderOtherTags is the discrepancy that
// motivated the check: several lanes measured under the minimal rung against a
// baseline taken under the full default set and read a 13-point drop that was
// never there.
func TestRun_CheckRefusesAProfileMeasuredUnderOtherTags(t *testing.T) {
	prof := writeProfile(t, 4, 10)
	base := filepath.Join(t.TempDir(), "baseline.txt")
	if err := run([]string{"-profile", prof, "-baseline", base, "-write", "-tags", "codec_json raft qrdb_sql", "-discovery-floor", "4"}, io.Discard); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := run([]string{"-profile", prof, "-baseline", base, "-min", "85", "-tags", "codec_json raft", "-discovery-floor", "4"}, io.Discard)
	var te *BuildTagsError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want *BuildTagsError", err)
	}
	if te.Baseline != "codec_json qrdb_sql raft" || te.Profile != "codec_json raft" {
		t.Errorf("BuildTagsError = %+v, want the two normalised sets", te)
	}
}

// TestRun_CheckRefusesABaselineThatRecordsNoTags: a baseline written before
// the tag set was recorded cannot say what it measured, so it is not
// comparable either.
func TestRun_CheckRefusesABaselineThatRecordsNoTags(t *testing.T) {
	base := filepath.Join(t.TempDir(), "baseline.txt")
	if err := os.WriteFile(base, []byte("example.com/m/p0 100.00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"-profile", writeProfile(t, 1, 10), "-baseline", base, "-min", "85", "-tags", "raft", "-discovery-floor", "1"}, io.Discard)
	var te *BuildTagsError
	if !errors.As(err, &te) || te.Baseline != "" {
		t.Fatalf("err = %v, want *BuildTagsError with no recorded set", err)
	}
}

// TestRun_TagsCompareAsASet: order, repetition and spacing are not a
// different measurement.
func TestRun_TagsCompareAsASet(t *testing.T) {
	prof := writeProfile(t, 2, 10)
	base := filepath.Join(t.TempDir(), "baseline.txt")
	if err := run([]string{"-profile", prof, "-baseline", base, "-write", "-tags", "raft codec_json raft", "-discovery-floor", "2"}, io.Discard); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := run([]string{"-profile", prof, "-baseline", base, "-min", "85", "-tags", "  codec_json   raft ", "-discovery-floor", "2"}, io.Discard); err != nil {
		t.Fatalf("check: %v", err)
	}
}

// TestRun_TagsAreRequired: a profile whose tag set is not declared cannot be
// compared, in either mode.
func TestRun_TagsAreRequired(t *testing.T) {
	prof := writeProfile(t, 1, 10)
	for name, args := range map[string][]string{
		"check": {"-profile", prof, "-baseline", "b", "-min", "91"},
		"write": {"-profile", prof, "-baseline", "b", "-write"},
		"blank": {"-profile", prof, "-baseline", "b", "-min", "91", "-tags", "   "},
	} {
		t.Run(name, func(t *testing.T) {
			var ce *ConfigError
			if err := run(args, io.Discard); !errors.As(err, &ce) {
				t.Fatalf("err = %v, want *ConfigError", err)
			}
		})
	}
}

// TestReadBaseline_BuildTagsDirective parses the directive and refuses a
// second one, which would leave the file saying two things.
func TestReadBaseline_BuildTagsDirective(t *testing.T) {
	b, err := ReadBaseline("b.txt", strings.NewReader("# header\n# build-tags: raft codec_json\nexample.com/m/a 91.50\n"))
	if err != nil {
		t.Fatalf("ReadBaseline: %v", err)
	}
	if b.Tags != "codec_json raft" || b.Held["example.com/m/a"] != 91.50 {
		t.Errorf("got %+v", b)
	}

	_, err = ReadBaseline("b.txt", strings.NewReader("# build-tags: raft\n# build-tags: raft\n"))
	var be *BaselineError
	if !errors.As(err, &be) || be.Line != 2 {
		t.Fatalf("err = %v, want *BaselineError at line 2", err)
	}
}

// TestWriteBaseline_RecordsTheTagSet: the directive is written, normalised,
// and reads back.
func TestWriteBaseline_RecordsTheTagSet(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteBaseline(&buf, map[string]Stats{"example.com/m/a": {Covered: 1, Total: 1}}, "raft codec_json"); err != nil {
		t.Fatalf("WriteBaseline: %v", err)
	}
	if !strings.Contains(buf.String(), "\n# build-tags: codec_json raft\n") {
		t.Errorf("no normalised directive in:\n%s", buf.String())
	}
	back, err := ReadBaseline("b.txt", &buf)
	if err != nil || back.Tags != "codec_json raft" {
		t.Fatalf("read back = %+v, %v", back, err)
	}
}

// TestBuildTagsError_Messages pins both messages a refused push prints.
func TestBuildTagsError_Messages(t *testing.T) {
	for _, tc := range []struct {
		err  *BuildTagsError
		want string
	}{
		{&BuildTagsError{Path: "b.txt", Baseline: "codec_json qrdb_sql raft", Profile: "codec_json raft"},
			"coverratchet: baseline b.txt was measured under build tags [codec_json qrdb_sql raft] but this profile under [codec_json raft]: " +
				"coverage depends on which files the tags compile, so the two are not comparable. " +
				"Measure with `make coverage`, or run `make coverage-baseline` if the tag set changed on purpose"},
		{&BuildTagsError{Path: "b.txt", Profile: "raft"},
			"coverratchet: baseline b.txt records no `# build-tags:` line, so what it measured is unknown: regenerate it with `make coverage-baseline`"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() =\n  %q\nwant\n  %q", got, tc.want)
		}
	}
}
