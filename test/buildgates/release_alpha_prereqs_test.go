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

package buildgates

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// releaseAlphaRule matches the release-alpha rule line. The target-specific
// `release-alpha: override export X := ...` lines contain `=` and are not
// matched, and `release-alpha-dry:` style decoys fail the `release-alpha:`
// anchor.
var releaseAlphaRule = regexp.MustCompile(`(?m)^release-alpha:([^=\n]*)$`)

// releaseAlphaPrereqDefect reports why the Makefile's release-alpha rule does
// not require the public-releasability scan and the tag lockstep check, or ""
// when it requires both. The release path has no other place that requires
// the scan: CI skips it without the pattern secret and release.yml skips it by
// design, so this prerequisite is what keeps a tag off an unscanned tree.
func releaseAlphaPrereqDefect(makefile []byte) string {
	m := releaseAlphaRule.FindSubmatch(makefile)
	if m == nil {
		return "Makefile has no release-alpha rule line"
	}
	have := map[string]bool{}
	for _, p := range strings.Fields(string(m[1])) {
		have[p] = true
	}
	for _, want := range []string{"check-public-releasable", "check-tag-lockstep"} {
		if !have[want] {
			return "release-alpha does not require " + want + ": " + strings.TrimSpace(string(m[0]))
		}
	}
	return ""
}

func TestReleaseAlphaRequiresThePublicReleasableScan(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if d := releaseAlphaPrereqDefect(b); d != "" {
		t.Fatalf("%s; a tag must be cut only from a tree the public-releasability scan passed", d)
	}
}

func TestReleaseAlphaPrereqGateSeesEachDrift(t *testing.T) {
	cases := []struct {
		name   string
		mk     string
		defect bool
	}{
		{"old rule without the scan", "release-alpha: check-tag-lockstep\n\t@true\n", true},
		{"scan and lockstep", "release-alpha: check-public-releasable check-tag-lockstep\n\t@true\n", false},
		{"scan alone", "release-alpha: check-public-releasable\n\t@true\n", true},
		{"only target-specific variables", "release-alpha: override export REL_VERSION := x\nrelease-alpha: override export REL_ORIGIN_VERSION := $(origin VERSION)\n", true},
		{"decoy target plus old rule", "release-alpha-dry: check-public-releasable check-tag-lockstep\nrelease-alpha: check-tag-lockstep\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := releaseAlphaPrereqDefect([]byte(c.mk))
			if c.defect && d == "" {
				t.Fatalf("gate saw no defect in %q", c.mk)
			}
			if !c.defect && d != "" {
				t.Fatalf("gate reported %q for a correct rule", d)
			}
		})
	}
}
