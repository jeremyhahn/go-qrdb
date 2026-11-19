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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Ruling 915: a pool leg pulls pool-images's image from the LAN CI registry
// with ci_registry_pull_id (scripts/image-registry.sh). Concurrent pulls of one
// image on a containerd-store daemon can lose the layer lock and fail (#1631,
// two of nine legs on one daemon), so the pull is tried up to three times about
// 10 s apart, each bounded by the registry timeout and checked against the
// image ID pool-images pushed; a wrong image or a stalled registry ends the
// attempts, and after the last one the leg fails closed (rulings 876, 879).

const pullID = "sha256:" + "1c50d8e67e019a0ce821db8720d148ea880a66d280b06ae723a306a4e9f73894"

// pullStubDocker answers `docker pull` by failing the first $STUB_FAIL pulls
// (printing the registry address on stderr, as docker does), sleeping
// $STUB_SLEEP seconds first when set, and counting every pull in
// $STUB_DIR/pulls; `docker image inspect` prints $STUB_ID once a pull has
// succeeded.
const pullStubDocker = `#!/bin/sh
case "$1" in
pull)
	n=$(cat "$STUB_DIR/pulls" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$STUB_DIR/pulls"
	[ -z "${STUB_SLEEP:-}" ] || sleep "$STUB_SLEEP"
	if [ "$n" -le "${STUB_FAIL:-0}" ]; then echo "Error response from daemon: Get \"http://192.168.77.7:5005/v2/\": connection reset" >&2; exit 1; fi
	: > "$STUB_DIR/pulled"; exit 0 ;;
image)
	[ -f "$STUB_DIR/pulled" ] || exit 1
	echo "$STUB_ID"; exit 0 ;;
esac
echo "stub docker: unexpected $*" >&2; exit 99
`

type pullCase struct {
	name      string
	fail      int
	id        string
	sleep     string
	wantOK    bool
	wantPulls int
	outage    bool
}

var pullCases = []pullCase{
	{name: "the first pull succeeds", fail: 0, id: pullID, wantOK: true, wantPulls: 1},
	{name: "two pulls lose the race, the third succeeds", fail: 2, id: pullID, wantOK: true, wantPulls: 3},
	{name: "every pull fails", fail: 9, id: pullID, wantOK: false, wantPulls: 3},
	{name: "the pull brings another image", fail: 0, id: "sha256:" + strings.Repeat("0", 64), wantOK: false, wantPulls: 1},
	{name: "the registry stalls", fail: 0, id: pullID, sleep: "4", wantOK: false, wantPulls: 1, outage: true},
}

// runPull runs ci_registry_pull_id from the given copy of image-registry.sh
// against the stub docker and returns its status, output, pull count and
// whether it left the outage marker.
func runPull(t *testing.T, script string, c pullCase) (bool, string, int, bool) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(pullStubDocker), 0o700); err != nil { // #nosec G306 -- the stub must be executable
		t.Fatal(err)
	}
	ref := "192.168.77.7:5005/go-qrdb/toolchain-image:1-1"
	cmd := exec.Command("bash", "-c", `. "$1" && ci_registry_pull_id "$2" "$3" "$4"`, "pull", script, "192.168.77.7:5005", ref, pullID) // #nosec G204 -- the repository's own script
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "STUB_DIR="+dir,
		"STUB_FAIL="+strconv.Itoa(c.fail), "STUB_ID="+c.id, "STUB_SLEEP="+c.sleep,
		"RUNNER_TEMP="+dir, "CI_IMAGE_REGISTRY_TIMEOUT=1", "CI_REGISTRY_PULL_PAUSE=0")
	out, err := cmd.CombinedOutput()
	raw, _ := os.ReadFile(filepath.Join(dir, "pulls"))
	pulls, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	matches, _ := filepath.Glob(filepath.Join(dir, "ci-image-registry-outage-*"))
	return err == nil, string(out), pulls, len(matches) > 0
}

// pullProblems runs every case against a copy of image-registry.sh and lists
// what it got wrong.
func pullProblems(t *testing.T, script string) []string {
	t.Helper()
	var problems []string
	for _, c := range pullCases {
		ok, out, pulls, outage := runPull(t, script, c)
		if ok != c.wantOK {
			problems = append(problems, fmt.Sprintf("%s: status ok=%v, want %v", c.name, ok, c.wantOK))
		}
		if pulls != c.wantPulls {
			problems = append(problems, fmt.Sprintf("%s: %d pulls, want %d", c.name, pulls, c.wantPulls))
		}
		if outage != c.outage {
			problems = append(problems, fmt.Sprintf("%s: outage marker %v, want %v", c.name, outage, c.outage))
		}
		if strings.Contains(out, "192.168.") {
			problems = append(problems, c.name+": prints the registry address: "+out)
		}
	}
	return problems
}

func TestRegistryPullID(t *testing.T) {
	script := filepath.Join(repoRoot(t), "scripts", "image-registry.sh")
	for _, p := range pullProblems(t, script) {
		t.Error(p)
	}
}

// TestRegistryPullID_CatchesRegressions: each regressed form of the helper
// fails the cases above.
func TestRegistryPullID_CatchesRegressions(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "image-registry.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct{ name, old, new string }{
		{name: "one attempt", old: "for attempt in 1 2 3; do", new: "for attempt in 1; do"},
		{name: "no ID check", old: `[ "$(docker image inspect -f '{{.Id}}' "$ref" 2>/dev/null)" = "$id" ]`, new: "true"},
		{name: "no fail-closed", old: "\treturn 1 # fail closed\n", new: "\treturn 0\n"},
		{name: "no timeout", old: `timeout -k 10 "$(ci_registry_timeout)" docker pull`, new: "docker pull"},
	} {
		t.Run(m.name, func(t *testing.T) {
			if !strings.Contains(string(src), m.old) {
				t.Fatalf("image-registry.sh has no %q: the case tests nothing", m.old)
			}
			mutated := filepath.Join(t.TempDir(), "image-registry.sh")
			if err := os.WriteFile(mutated, []byte(strings.Replace(string(src), m.old, m.new, 1)), 0o600); err != nil { // #nosec G703 -- a file in the test's own temporary directory
				t.Fatal(err)
			}
			if len(pullProblems(t, mutated)) == 0 {
				t.Fatalf("a helper with %s passes every case", m.name)
			}
		})
	}
}
