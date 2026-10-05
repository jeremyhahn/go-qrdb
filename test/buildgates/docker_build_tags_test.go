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

// docker_build_tags_test.go closes go-qrdb#555: the standalone integration
// stage's qrdbd and qrdb binaries were baked from a literal, narrower tag
// list in .devcontainer/Dockerfile ('codec_json raft qrdb_ts qrdb_vector' for
// qrdbd, 'qrdb_ts qrdb_vector' for qrdb) that nothing kept in step with the
// Makefile's BUILD_TAGS. Fourteen standalone tests failed on "unknown flag"
// for --sql-concurrency, --etcd, --mysql, --postgresql and update-raft
// because the server and the CLI that drove it had each compiled a
// different, incomplete feature set.
//
// The fix makes the Dockerfile take the tag set as an ARG
// (QRDB_BUILD_TAGS), which `make integration-image` overrides with
// BUILD_TAGS itself (one Makefile variable, no second hardcoded list on
// the make side). The ARG still carries a literal DEFAULT -- #302 requires
// a build that skips make (VS Code "Reopen in Container", a bare `docker
// compose build`) keep working -- and that default is the second copy this
// gate exists to hold in step, the same role check-tool-versions.sh plays
// for the scanner-version ARGs right above it in the same file.

// dockerfilePath is .devcontainer/Dockerfile, relative to the repo root.
func dockerfilePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), ".devcontainer", "Dockerfile")
}

// qrdbBuildTagsArgDefault reads `ARG QRDB_BUILD_TAGS="..."`'s default value
// out of .devcontainer/Dockerfile. A lookup that finds nothing is a FAILURE,
// not a vacuous pass: the pattern depends on a file path and a spelling that
// a future edit could move, and a gate that silently compares two empty
// strings would stop testing anything while still reporting green.
func qrdbBuildTagsArgDefault(t *testing.T) string {
	t.Helper()
	path := dockerfilePath(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	re := regexp.MustCompile(`(?m)^ARG QRDB_BUILD_TAGS="([^"]*)"\s*$`)
	m := re.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("no 'ARG QRDB_BUILD_TAGS=\"...\"' line found in %s -- the pattern that reads it has "+
			"stopped matching (a renamed ARG or a reformatted default), which is a failure here and not "+
			"a pass: an empty value would otherwise compare equal to another empty value", path)
	}
	return m[1]
}

// qrdbBuildTagsGoBuildCount counts how many `go build` invocations in
// .devcontainer/Dockerfile pass `-tags "${QRDB_BUILD_TAGS}"`. The gate
// requires at least 2 (qrdbd and qrdb): a literal tag list reintroduced on
// either `go build` line -- the exact regression #555 fixed -- would leave
// this below the floor.
func qrdbBuildTagsGoBuildCount(t *testing.T) int {
	t.Helper()
	path := dockerfilePath(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Count(string(data), `-tags "${QRDB_BUILD_TAGS}"`)
}

// TestDockerBuildTags_ArgDefaultMatchesMakefile asserts
// .devcontainer/Dockerfile's QRDB_BUILD_TAGS ARG default is exactly the set
// `make -s print-build-tags` prints -- byte for byte, not just as a set:
// the two are maintained by hand in two files, and a byte-for-byte match is
// what lets a reviewer diff them on sight rather than having to evaluate
// set equality mentally.
func TestDockerBuildTags_ArgDefaultMatchesMakefile(t *testing.T) {
	want := strings.Join(fullBuildTags(t), " ")
	got := qrdbBuildTagsArgDefault(t)
	if got != want {
		t.Fatalf("`ARG QRDB_BUILD_TAGS` default in .devcontainer/Dockerfile is %q, but "+
			"`make -s print-build-tags` prints %q. These must agree: `make integration-image` "+
			"overrides the ARG with the Makefile's own value, but a bare `docker compose build` or "+
			"VS Code \"Reopen in Container\" (#302) falls back to the Dockerfile's default alone, so a "+
			"stale default silently bakes qrdbd/qrdb with the wrong feature set in that path.", got, want)
	}
}

// TestDockerBuildTags_BothBinariesShareTheArg asserts both `go build`
// invocations in the integration stage use `-tags "${QRDB_BUILD_TAGS}"`
// rather than a literal list -- the qrdbd daemon and the qrdb CLI must
// compile the same feature set, or the CLI offers a flag the server it
// drives never compiled a handler for (or refuses one the server supports).
func TestDockerBuildTags_BothBinariesShareTheArg(t *testing.T) {
	const wantCount = 2 // qrdbd and qrdb
	if got := qrdbBuildTagsGoBuildCount(t); got != wantCount {
		t.Fatalf("found %d `go build ... -tags \"${QRDB_BUILD_TAGS}\"` invocations in "+
			"%s, want %d (qrdbd and qrdb). A count below %d means at least one binary's `-tags` was "+
			"replaced with a literal list, reintroducing the drift go-qrdb#555 fixed; a count above means "+
			"this gate's floor needs raising for a newly added binary.", got, dockerfilePath(t), wantCount, wantCount)
	}
}
