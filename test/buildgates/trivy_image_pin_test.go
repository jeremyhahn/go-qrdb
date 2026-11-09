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
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Trivy is installed from its official image, pinned by digest in
// scripts/tool-pins.sh. The GitHub release download it replaced returned 504
// for every Trivy asset and failed the CI image build, so no install path may
// go back to it, nor to upstream's install script, which reads the same
// endpoint.

// trivyReleaseFetch matches a fetch of a Trivy release asset or of the
// upstream install script.
// The pattern is split so that a tree-wide grep for the fetch finds no hit
// in this test.
var trivyReleaseFetch = regexp.MustCompile(`aquasecurity/trivy/` + `releases/download|trivy/[^\s"']*/?` + `contrib/install\.sh`)

func TestTrivyImage_PinnedByDigest(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "tool-pins.sh"))
	if err != nil {
		t.Fatalf("read tool pins: %v", err)
	}
	m := regexp.MustCompile(`(?m)^TRIVY_IMAGE=(\S+)$`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("scripts/tool-pins.sh pins no TRIVY_IMAGE")
	}
	if !regexp.MustCompile(`@sha256:[0-9a-f]{64}$`).Match(m[1]) {
		t.Errorf("TRIVY_IMAGE %q is not pinned by an @sha256: digest", m[1])
	}
	v := regexp.MustCompile(`(?m)^TRIVY_VERSION=v(\S+)$`).FindSubmatch(raw)
	if v == nil {
		t.Fatal("scripts/tool-pins.sh pins no TRIVY_VERSION")
	}
	if !strings.Contains(string(m[1]), ":"+string(v[1])+"@") {
		t.Errorf("TRIVY_IMAGE %q does not carry the tag of TRIVY_VERSION v%s", m[1], v[1])
	}
}

func TestTrivyImage_NoReleaseDownload(t *testing.T) {
	root := repoRoot(t)
	var scanned []string
	for _, dir := range []string{".github", "build", ".devcontainer"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			scanned = append(scanned, path)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	scanned = append(scanned, filepath.Join(root, "Makefile"))
	for _, path := range scanned {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if trivyReleaseFetch.MatchString(line) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s:%d fetches Trivy from its release endpoint; copy it out of TRIVY_IMAGE instead: %s", rel, i+1, strings.TrimSpace(line))
			}
		}
	}
}
