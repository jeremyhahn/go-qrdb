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
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// intNamesProbe is a makefile read after the repository's own: its one
// target prints the names an integration run creates on the host, as the
// Makefile derives them, without running any recipe of the Makefile.
const intNamesProbe = "int-names-probe:\n\t@echo \"$(INT_PROJECT) $(INT_IMAGE) $(INT_GPU_IMAGE)\"\n"

// composeProjectName is what `docker compose -p` accepts.
var composeProjectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// intJobNames returns the compose project and image names the Makefile
// derives for `make integration-test-<feature>` run as attempt attempt of
// CI run runID. MAKECMDGOALS is passed rather than the goal itself, because
// a feature's recipe may start a compose stack.
func intJobNames(t *testing.T, root, runID, attempt, feature string) []string {
	t.Helper()
	return intGoalNames(t, root, runID, attempt, "integration-test-"+feature)
}

// intGoalNames is intJobNames for any goal: the names the Makefile derives
// when goal is the command line's goal.
func intGoalNames(t *testing.T, root, runID, attempt, goal string) []string {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe.mk")
	if err := os.WriteFile(probe, []byte(intNamesProbe), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("make", "-s", "-f", "Makefile", "-f", probe, "MAKECMDGOALS="+goal, "int-names-probe") // #nosec G204 -- probe is this test's own temp file, goal one of its own make goals
	cmd.Dir = root
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "INT_PROJECT=") { // an inherited override would hide the derivation
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GITHUB_RUN_ID="+runID, "GITHUB_RUN_ATTEMPT="+attempt)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("make int-names-probe for %s: %v\n%s", goal, err, errOut.String())
	}
	names := strings.Fields(out.String())
	if len(names) != 3 {
		t.Fatalf("probe for %s printed %q, want project, image and GPU image", goal, out.String())
	}
	return names
}

// TestIntegrationJobNames_UniquePerFeature holds CI's one-job-per-feature
// legs apart on one runner host: each leg, and each attempt of a leg, must
// derive its own compose project and image tags, or one leg's build and
// teardown land on another's (run 51344: "image ... already exists").
func TestIntegrationJobNames_UniquePerFeature(t *testing.T) {
	root := repoRoot(t)
	features := makefileFeatures(t, root)
	if len(features) < 2 {
		t.Fatalf("make print-integration-features listed %d features; the gate needs at least two", len(features))
	}
	owner := make(map[string]string) // name -> the leg that derived it
	claim := func(leg string, names []string) {
		for _, n := range names {
			if prev, ok := owner[n]; ok {
				t.Errorf("legs %s and %s both derive %q", prev, leg, n)
			}
			owner[n] = leg
		}
	}
	for _, f := range features {
		names := intJobNames(t, root, "51344", "1", f)
		if !composeProjectName.MatchString(names[0]) {
			t.Errorf("leg %s: compose project %q is not a valid project name", f, names[0])
		}
		for _, img := range names[1:] {
			if tag := img[strings.LastIndex(img, ":")+1:]; len(tag) > 128 {
				t.Errorf("leg %s: image tag %q exceeds 128 characters", f, tag)
			}
		}
		claim(f+" attempt 1", names)
	}
	claim(features[0]+" attempt 2", intJobNames(t, root, "51344", "2", features[0]))
	claim(features[0]+" of another run", intJobNames(t, root, "51345", "1", features[0]))
}

// TestIntegrationJobNames_StablePerJob: the teardown is a second make
// invocation, so a job must derive the same names every time it asks, or
// integration-down removes nothing of its own.
func TestIntegrationJobNames_StablePerJob(t *testing.T) {
	root := repoRoot(t)
	first := intJobNames(t, root, "51344", "1", "rag")
	again := intJobNames(t, root, "51344", "1", "rag")
	if strings.Join(first, " ") != strings.Join(again, " ") {
		t.Fatalf("one job derived %q, then %q", first, again)
	}
	if !strings.HasSuffix(first[0], "-51344-1-rag") {
		t.Fatalf("project %q does not carry the run, attempt and feature", first[0])
	}
}

// TestIntegrationJobNames_TeardownMatchesLeg: a leg's always() teardown step
// runs `make integration-down-<feature>`, a different goal from the test
// step's, and must still derive that leg's names -- and no other leg's -- or
// a cancelled leg's stack and images stay on the runner for good.
func TestIntegrationJobNames_TeardownMatchesLeg(t *testing.T) {
	root := repoRoot(t)
	for _, f := range []string{"rag", "key-value"} {
		leg := intJobNames(t, root, "51344", "1", f)
		down := intGoalNames(t, root, "51344", "1", "integration-down-"+f)
		if strings.Join(leg, " ") != strings.Join(down, " ") {
			t.Errorf("leg %s derived %q, its teardown %q", f, leg, down)
		}
	}
	bare := intGoalNames(t, root, "51344", "1", "integration-down")
	if leg := intJobNames(t, root, "51344", "1", "rag"); bare[0] == leg[0] {
		t.Fatalf("bare integration-down derived the rag leg's project %q: the check cannot tell legs apart", bare[0])
	}
}
