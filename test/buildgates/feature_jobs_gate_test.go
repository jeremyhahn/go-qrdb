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

// CI runs the self-spawned integration suites (test/integration) as a few
// shards of features: ci.yml's integration-features job is a matrix whose
// include entries each name a shard and its feature list, and each shard runs
// `make integration-test-features IT_FEATURES_IN=image` over that list --
// `make integration-test IT_FEATURE=<feature>` per feature, the selection
// `make integration-test-<feature>` runs on a host, in the integration image,
// going on past a failing one -- under its own limit, in parallel (owner
// ruling 10, 2026-10-14; sharded so the per-job setup is paid once per shard).
// The shards together must run every feature test/integration/features.mk
// maps at least one file to, each exactly once;
// `make -s print-integration-features` prints them.
//
// The lists are literal in ci.yml (handbook devops.md §1), so each shard's
// features read in the workflow itself, and this gate is what keeps their
// union equal to the Makefile's. Each property below is one edit from going
// false without anything else noticing:
//
//   - a feature mapped in features.mk but in no shard: its tests never run
//     in CI, and feature_integration_coverage_test.go still passes, because
//     it checks the mapping, not the jobs;
//   - a feature in two shards, which runs twice, or a listed name whose
//     features.mk entry is empty or absent, which runs zero tests;
//   - a limit above featureJobLimit minutes, or none at all (GitHub's default is 360);
//   - fail-fast left at its default, true, so one red shard cancels the
//     others and hides whether they would have passed;
//   - a shard that runs something other than the multi-feature target, or a job
//     that leaves the full tier or stops running on push;
//   - a shard with no always() teardown, so a cancelled shard leaves its compose
//     project on the runner;
//   - a shard whose project name another shard or run shares;
//   - a shard that runs any image but the one docker-build built (#813,
//     DS-3516, DS-7377): every shard of a run downloads docker-build's
//     it-image artifact, loads it against docker-build's it_id, and runs the
//     tests with IT_PREBUILT=1, so make refuses to build an image of its own.
//     ci_image_digest_test.go holds every consumer to the hand-off; this gate
//     holds the shard's steps to it exactly.
//
// That a shard fails when one of its features fails is the Makefile's
// integration-test-features; that the gate goes red when one shard fails is
// ci_tiers_test.go's TestCIGateScriptHonoursTheTiers: a matrix is one needs
// entry, and its result is failure when any shard fails.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	// featureJob is ci.yml's sharded per-feature integration job.
	featureJob = "integration-features"

	// featureJobLimit is each shard's timeout-minutes ceiling. Hosted
	// runners have 4 vCPUs (DS-7377), and every CI estimate is planned at a 4x
	// slowdown over the measured self-hosted time: the longest shard measured
	// 21.2 minutes (run 76127), 85 at 4x, and the limit leaves room above it
	// while staying far under the hosted runner's 360.
	featureJobLimit = 120

	// featureShardKey and featureListKey are the two keys of each of the
	// matrix's include entries: the shard's name and its space-separated
	// feature list.
	featureShardKey = "shard"
	featureListKey  = "features"

	// featureListEnv is the job env entry that hands the shard's list to
	// featureStep through the shell, never a ${{ }} spliced into the command.
	featureListEnv = "${{ matrix." + featureListKey + " }}"

	// featureProjectPrefix is the name prefix every shard's compose project
	// carries.
	featureProjectPrefix = "qrdb-ci-"

	// freeDiskStepName is the step every hosted job that runs a Docker stage
	// opens with: it deletes the runner's preinstalled SDKs to make room for
	// the images. Like the credential mask, it runs no make target.
	freeDiskStepName = "Free the runner's disk"

	// featureLoad is each shard's first step after the artifact download: it
	// loads docker-build's integration image, checks it against
	// docker-build's image ID, and hands its tag to the tests as IT_IMAGE.
	featureLoad = "scripts/ci-image.sh load \"$RUNNER_TEMP/it-image\" \"$IMAGE\" \"$ID\"\necho \"IT_IMAGE=$IMAGE\" >> \"$GITHUB_ENV\""

	// featureStep is the one test command each shard runs: every feature in
	// its list, each the feature's features.mk selection in the integration
	// image, under the shard's project, past a failing one. The image is the
	// job's IT_IMAGE, which the load step set; IT_PREBUILT=1 makes the
	// Makefile refuse to build one in its place.
	featureStep = `make integration-test-features IT_FEATURES="$IT_FEATURES" IT_FEATURES_IN=image IT_PROJECT="$IT_PROJECT" IT_PREBUILT=1`

	// featureTeardown is the shard's last step, run under always(): it removes
	// the compose project featureStep ran under, so a cancelled shard's
	// containers stop before the runner is recycled.
	featureTeardown = `make docker-down-it IT_PROJECT="$IT_PROJECT"`

	// featureImageRepo is the repository of `make -s it-image`, the family
	// scripts/ci-image.sh hands on.
	featureImageRepo = "qrdb-ci-it"

	// featureLoadImage and featureLoadID are the load step's env:
	// docker-build's integration image and its ID, one image for every shard
	// of the run.
	featureLoadImage = "${{ needs.docker-build.outputs.it_image }}"
	featureLoadID    = "${{ needs.docker-build.outputs.it_id }}"

	// featureLegFloor is a discovery floor: 40 features map files today, and
	// a parse that finds fewer than this has found too little to check.
	featureLegFloor = 30
)

// featureWorkflow is the slice of ci.yml this gate reads.
type featureWorkflow struct {
	On   yaml.Node `yaml:"on"`
	Jobs map[string]struct {
		If             string            `yaml:"if"`
		Needs          []string          `yaml:"needs"`
		TimeoutMinutes *string           `yaml:"timeout-minutes"`
		Env            map[string]string `yaml:"env"`
		Strategy       struct {
			FailFast *bool     `yaml:"fail-fast"`
			Matrix   yaml.Node `yaml:"matrix"`
		} `yaml:"strategy"`
		Steps []struct {
			Name string            `yaml:"name"`
			If   string            `yaml:"if"`
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			With map[string]string `yaml:"with"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// featureLimitInRange reports whether a shard's timeout-minutes is a literal
// whole number in 1..featureJobLimit. The field is read as a string because
// another job's limit may be an expression (the fips legs' is
// `${{ matrix.minutes }}`), which would fail an int decode of the whole file.
func featureLimitInRange(tm string) bool {
	n, err := strconv.Atoi(tm)
	return err == nil && n > 0 && n <= featureJobLimit
}

// featureShardFix is the fix the gate prints for a feature no shard runs.
const featureShardFix = "add it to the " + featureListKey + ": of the shard with the least expected time " +
	"(docs/release/engineering.md, \"Per-feature integration jobs\")"

// featureJobProblems checks ci.yml's integration-features job against want,
// the features that must each run in exactly one shard.
func featureJobProblems(body []byte, want []string) []string {
	var wf featureWorkflow
	if err := yaml.Unmarshal(body, &wf); err != nil {
		return []string{"ci.yml is not a workflow this gate can read: " + err.Error()}
	}
	var problems []string
	triggers := onTriggers(wf.On)
	for _, ev := range []string{"push", "pull_request"} {
		if !slices.Contains(triggers, ev) {
			problems = append(problems, "ci.yml does not run on "+ev+": "+featureJob+" would not gate it")
		}
	}
	job, ok := wf.Jobs[featureJob]
	if !ok {
		return append(problems, "ci.yml has no "+featureJob+" job: no feature's integration tests run in CI")
	}
	if strings.TrimSpace(withoutDraftSkip(job.If)) != fullTierIf {
		problems = append(problems, featureJob+"'s if: is "+strconv.Quote(withoutDraftSkip(job.If))+"; want the full-tier condition")
	}
	switch tm := job.TimeoutMinutes; {
	case tm == nil:
		problems = append(problems, featureJob+" has no timeout-minutes: a shard would get the runner's default, not its own limit")
	case !featureLimitInRange(*tm):
		problems = append(problems, featureJob+"'s timeout-minutes is "+*tm+
			"; want 1.."+strconv.Itoa(featureJobLimit)+": the shard ceiling, derived in featureJobLimit's comment")
	}
	if ff := job.Strategy.FailFast; ff == nil || *ff {
		problems = append(problems, featureJob+" does not set fail-fast: false: one red shard would cancel the rest")
	}
	problems = append(problems, featureShardProblems(job.Strategy.Matrix, want)...)
	if v := job.Env["IT_FEATURES"]; v != featureListEnv {
		problems = append(problems, featureJob+"'s IT_FEATURES is "+strconv.Quote(v)+
			"; want "+strconv.Quote(featureListEnv)+", the list featureStep runs")
	}

	var runs, conds []string
	var downloaded bool
	for _, s := range job.Steps {
		// The credential mask (ci-template v1.21.1) and the disk cleanup lead
		// the shard; the mask's shape is ci_logs_test.go's to check. Only a run
		// step before any other that runs no make target is taken for one, so
		// the name cannot carry a narrowed run past this gate.
		if len(runs) == 0 && (s.Name == maskStepName || s.Name == freeDiskStepName) && !strings.Contains(s.Run, "make ") {
			continue
		}
		if strings.HasPrefix(s.Uses, downloadArtifactAction) && len(runs) == 0 {
			downloaded = s.With["name"] == "it-image"
		}
		if s.Run != "" {
			runs = append(runs, strings.TrimSpace(s.Run))
			conds = append(conds, strings.TrimSpace(s.If))
		}
	}
	if !downloaded {
		problems = append(problems, featureJob+" downloads no it-image artifact before its first step: "+
			"the load would find no image to load")
	}
	if !slices.Equal(runs, []string{featureLoad, featureStep, featureTeardown}) {
		problems = append(problems, featureJob+" runs "+strconv.Quote(strings.Join(runs, "; "))+
			"; want exactly `"+featureLoad+"`, `"+featureStep+"`, then `"+featureTeardown+"`")
	} else if !slices.Equal(conds, []string{"", "", "always()"}) {
		problems = append(problems, featureJob+"'s steps run under if: "+strconv.Quote(strings.Join(conds, ", "))+
			"; want the load and the tests unconditional and the teardown under always(), "+
			"so a failed or cancelled shard still removes its stack")
	}
	for _, s := range job.Steps {
		if strings.TrimSpace(s.Run) != featureLoad {
			continue
		}
		if s.Env["IMAGE"] != featureLoadImage || s.Env["ID"] != featureLoadID {
			problems = append(problems, featureJob+"'s load step sets IMAGE "+strconv.Quote(s.Env["IMAGE"])+" and ID "+
				strconv.Quote(s.Env["ID"])+"; want "+featureLoadImage+" and "+featureLoadID+
				": every shard runs the one image docker-build built, checked by its ID")
		}
	}
	// The project the steps pass must be this shard's alone: shards of one run,
	// and runs sharing a daemon, would otherwise share a stack.
	v := job.Env["IT_PROJECT"]
	if !strings.HasPrefix(v, featureProjectPrefix) {
		problems = append(problems, featureJob+"'s IT_PROJECT is "+strconv.Quote(v)+
			"; want it under "+strconv.Quote(featureProjectPrefix))
	}
	for _, part := range []string{"${{ github.run_id }}", "${{ github.run_attempt }}", "${{ matrix." + featureShardKey + " }}"} {
		if !strings.Contains(v, part) {
			problems = append(problems, featureJob+"'s IT_PROJECT is "+strconv.Quote(v)+
				"; want it to carry "+part+", so no other shard or run shares it")
		}
	}
	// The image is the opposite: one image for every shard of the run (#813).
	// The load step sets IT_IMAGE through GITHUB_ENV; a job-level value would
	// name an image before any is loaded and, being defined, would override
	// the Makefile's content-hash default.
	if img, set := job.Env["IT_IMAGE"]; set {
		problems = append(problems, featureJob+" sets IT_IMAGE "+strconv.Quote(img)+
			" at job level; want it set by the load step alone, which knows the image this shard loaded")
	}
	if !slices.Contains(job.Needs, "docker-build") {
		problems = append(problems, featureJob+" does not need docker-build: its IT_IMAGE output would be empty")
	}
	return problems
}

// featureShardProblems checks that the matrix is an include list of shards,
// each a name and a feature list, and that the lists together name every
// feature in want exactly once and nothing else; each problem prints its fix.
func featureShardProblems(m yaml.Node, want []string) []string {
	if m.Kind != yaml.MappingNode {
		return []string{featureJob + " has no matrix mapping; want an include: list of " +
			featureShardKey + " and " + featureListKey + " entries"}
	}
	var problems []string
	var shards []string
	shardOf := map[string]string{}
	found := false
	for i := 0; i+1 < len(m.Content); i += 2 {
		key, val := m.Content[i].Value, m.Content[i+1]
		if key != "include" {
			problems = append(problems, featureJob+"'s matrix has key "+strconv.Quote(key)+
				"; want only include: any axis multiplies or edits the shards")
			continue
		}
		found = true
		if val.Kind != yaml.SequenceNode {
			problems = append(problems, featureJob+"'s matrix include is not a literal list")
			continue
		}
		for _, e := range val.Content {
			name, list, p := featureShardEntry(e)
			if p != "" {
				problems = append(problems, p)
				continue
			}
			if slices.Contains(shards, name) {
				problems = append(problems, featureJob+" has two shards named "+strconv.Quote(name)+
					"; fix: rename one: the shard name is part of its compose project")
				continue
			}
			shards = append(shards, name)
			for _, f := range strings.Fields(list) {
				if prev, dup := shardOf[f]; dup {
					problems = append(problems, featureJob+" runs "+f+" in shard "+prev+" and again in shard "+name+
						"; fix: delete it from shard "+name)
					continue
				}
				shardOf[f] = name
				if !slices.Contains(want, f) {
					problems = append(problems, featureJob+"'s shard "+name+" lists "+strconv.Quote(f)+
						", which `make -s print-integration-features` does not print; fix: delete it from shard "+
						name+" (features.mk maps no test file to it)")
				}
			}
		}
	}
	if !found {
		return append(problems, featureJob+"'s matrix has no include: list of shards")
	}
	for _, f := range want {
		if _, ok := shardOf[f]; !ok {
			problems = append(problems, featureJob+" runs no shard for "+f+
				", which `make -s print-integration-features` prints; fix: "+featureShardFix)
		}
	}
	return problems
}

// featureShardEntry reads one include entry: exactly a literal shard name and
// a literal, non-empty feature list.
func featureShardEntry(e *yaml.Node) (name, list, problem string) {
	if e.Kind != yaml.MappingNode {
		return "", "", featureJob + "'s matrix include has an entry that is not a mapping"
	}
	got := map[string]*yaml.Node{}
	for i := 0; i+1 < len(e.Content); i += 2 {
		k := e.Content[i].Value
		if k != featureShardKey && k != featureListKey {
			return "", "", featureJob + "'s shard entry has key " + strconv.Quote(k) +
				"; want only " + featureShardKey + " and " + featureListKey
		}
		got[k] = e.Content[i+1]
	}
	for _, k := range []string{featureShardKey, featureListKey} {
		if n := got[k]; n == nil || n.Kind != yaml.ScalarNode || strings.TrimSpace(n.Value) == "" {
			return "", "", featureJob + "'s shard entry has no literal " + k
		}
	}
	return got[featureShardKey].Value, got[featureListKey].Value, ""
}

// makefileFeatures returns `make -s print-integration-features`, the one
// source of the features the shards must run.
func makefileFeatures(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("make", "-s", "print-integration-features")
	cmd.Dir = root
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("make -s print-integration-features: %v\n%s", err, errOut.String())
	}
	return strings.Fields(out.String())
}

func TestIntegrationFeatureCoverage_EveryFeatureInOneShard(t *testing.T) {
	root := repoRoot(t)
	want := makefileFeatures(t, root)

	// The Makefile's list must be the features.mk entries that map a file,
	// read independently, so a broken INT_FEATURES cannot agree with itself.
	files, _ := featureMapping(t, root)
	mapped := make([]string, 0, len(files))
	for v := range files {
		mapped = append(mapped, strings.ReplaceAll(strings.TrimPrefix(v, intTestVarPrefix), "_", "-"))
	}
	sort.Strings(mapped)
	if !slices.Equal(want, mapped) {
		t.Fatalf("make -s print-integration-features printed %v; features.mk maps files for %v", want, mapped)
	}
	if len(want) < featureLegFloor {
		t.Fatalf("found %d features with integration tests, below the floor of %d: a gate that finds "+
			"nothing to check reports the same green as one that checked everything", len(want), featureLegFloor)
	}

	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	for _, p := range featureJobProblems(body, want) {
		t.Error(p)
	}
}

// makeOutput runs `make -s args...` at root and returns its trimmed stdout.
func makeOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("make", append([]string{"-s"}, args...)...) // #nosec G204 -- args are this file's own make target and variable names
	cmd.Dir = root
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("make -s %s: %v\n%s", strings.Join(args, " "), err, errOut.String())
	}
	return strings.TrimSpace(out.String())
}

// The legs name no image, so the Makefile's name is the one they share: it
// must be content-addressed (the same on every call for one tree) and sit
// under the prefix the reap step removes images under (#813).
func TestIntegrationFeatureCoverage_ImageIsContentAddressed(t *testing.T) {
	root := repoRoot(t)
	first := makeOutput(t, root, "it-image")
	tag, ok := strings.CutPrefix(first, featureImageRepo+":")
	if !ok {
		t.Fatalf("make -s it-image = %q; want %s:<hash>, under the reap step's prefix", first, featureImageRepo)
	}
	if len(tag) != 16 || strings.Trim(tag, "0123456789abcdef") != "" {
		t.Fatalf("make -s it-image tag = %q; want the 16-hex content hash", tag)
	}
	if again := makeOutput(t, root, "it-image"); again != first {
		t.Fatalf("make -s it-image printed %q, then %q: legs of one run would name different images", first, again)
	}
	if got := makeOutput(t, root, "it-image", "IT_IMAGE=qrdb-ci-it:override"); got != "qrdb-ci-it:override" {
		t.Fatalf("make -s it-image IT_IMAGE=... = %q; want the caller's name", got)
	}
}

// docker-down-it removes a shard's stack and never the shared image: a shard that
// removed it would make every later leg on the runner rebuild it.
func TestIntegrationFeatureCoverage_DownKeepsTheImage(t *testing.T) {
	root := repoRoot(t)
	recipe := makeOutput(t, root, "-n", "docker-down-it", "IT_PROJECT=qrdb-ci-x")
	if !strings.Contains(recipe, "-p 'qrdb-ci-x'") || !strings.Contains(recipe, "down --volumes") {
		t.Fatalf("make -n docker-down-it = %q; want the shard's compose project downed", recipe)
	}
	if strings.Contains(recipe, "image rm") || strings.Contains(recipe, "rmi") {
		t.Fatalf("make -n docker-down-it = %q; want no image removal: the legs share the image", recipe)
	}
}

// buildRaceDocker is a stub docker for docker-build-it: `buildx version`
// succeeds, `buildx build` records that it ran and fails with status 17 the
// way the second of two concurrent --load builds of one tag does, and `image
// inspect` finds the image only once a build has run and STUB_LOADED is 1.
const buildRaceDocker = `#!/bin/sh
case "$1 $2" in
"buildx version") exit 0 ;;
"buildx build")
	: > "$STUB_DIR/built"
	echo 'ERROR: failed to build: failed to solve: image "docker.io/library/qrdb-ci-it:test": already exists' >&2
	exit 17 ;;
"image inspect")
	[ -f "$STUB_DIR/built" ] && [ "$STUB_LOADED" = 1 ] && exit 0
	exit 1 ;;
esac
echo "stub docker: unexpected $*" >&2
exit 99
`

// Two legs that miss the image together on one daemon both build it, and the
// second --load fails because the first already tagged it (run 57133). The
// loser must succeed when the daemon then holds the content-tagged image, and
// any other failed build must still fail with the build's own status.
func TestIntegrationFeatureCoverage_BuildSurvivesAConcurrentLoad(t *testing.T) {
	root := repoRoot(t)
	for _, tc := range []struct {
		name    string
		loaded  string
		wantErr bool
	}{
		{name: "a concurrent leg loaded the image", loaded: "1", wantErr: false},
		{name: "the build failed and no image is held", loaded: "0", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := t.TempDir()
			if err := os.WriteFile(filepath.Join(stub, "docker"), []byte(buildRaceDocker), 0o700); err != nil { // #nosec G306 -- the stub must be executable
				t.Fatalf("write stub docker: %v", err)
			}
			envFile := filepath.Join(stub, "module.env")
			if err := os.WriteFile(envFile, []byte("GIT_CONFIG_KEY_0=stub\n"), 0o600); err != nil {
				t.Fatalf("write module env: %v", err)
			}
			cmd := exec.Command("make", "-s", "docker-build-it", "IT_IMAGE=qrdb-ci-it:test", "MODULE_ENV_FILE="+envFile) // #nosec G204 -- a fixed target and a tempdir path
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"),
				"STUB_DIR="+stub, "STUB_LOADED="+tc.loaded)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			err := cmd.Run()
			if _, statErr := os.Stat(filepath.Join(stub, "built")); statErr != nil {
				t.Fatalf("make docker-build-it never ran the build:\n%s", out.String())
			}
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("make docker-build-it: %v; want success once a concurrent leg loaded the image:\n%s", err, out.String())
				}
				return
			}
			if err == nil {
				t.Fatalf("make docker-build-it succeeded with no image held; want the build's failure:\n%s", out.String())
			}
			if !strings.Contains(out.String(), "Error 17") {
				t.Fatalf("make docker-build-it failed without the build's status 17:\n%s", out.String())
			}
		})
	}
}

const featureJobFixture = `on:
  push:
    branches: [main]
  pull_request:
    types: [opened, synchronize, reopened]
jobs:
  integration-features:
    name: Integration (shard ${{ matrix.shard }})
    if: (github.event_name != 'pull_request' && (github.event_name != 'workflow_dispatch' || github.ref == 'refs/heads/main')) || contains(github.event.pull_request.labels.*.name, 'ci:full')
    needs: [docker-build, arch-gates]
    timeout-minutes: 120
    env:
      IT_PROJECT: qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.shard }}
      IT_FEATURES: ${{ matrix.features }}
    strategy:
      fail-fast: false
      matrix:
        include:
          - shard: 1
            features: audit sql
          - shard: 2
            features: key-value
    steps:
      - uses: actions/checkout@0000000000000000000000000000000000000000 # v6
      - name: Mask the forge credentials
        run: echo "::add-mask::$GIT_CONFIG_VALUE_0"
      - name: Free the runner's disk
        run: sudo rm -rf /usr/share/dotnet
      - name: Download the integration image
        uses: actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0
        with:
          name: it-image
          path: ${{ runner.temp }}/it-image
      - name: Load the integration image
        env:
          IMAGE: ${{ needs.docker-build.outputs.it_image }}
          ID: ${{ needs.docker-build.outputs.it_id }}
        run: |
          scripts/ci-image.sh load "$RUNNER_TEMP/it-image" "$IMAGE" "$ID"
          echo "IT_IMAGE=$IMAGE" >> "$GITHUB_ENV"
      - name: Integration tests (shard ${{ matrix.shard }})
        run: make integration-test-features IT_FEATURES="$IT_FEATURES" IT_FEATURES_IN=image IT_PROJECT="$IT_PROJECT" IT_PREBUILT=1
      - name: Tear down (shard ${{ matrix.shard }})
        if: always()
        run: make docker-down-it IT_PROJECT="$IT_PROJECT"
`

var featureFixtureWant = []string{"audit", "key-value", "sql"}

func TestIntegrationFeatureCoverage_JobProblemsValidFixture(t *testing.T) {
	if p := featureJobProblems([]byte(featureJobFixture), featureFixtureWant); len(p) != 0 {
		t.Fatalf("the valid fixture must have no problems, got %v", p)
	}
}

// TestIntegrationFeatureCoverage_ShardProblemsPrintTheFix proves each way the
// shard lists can drift from `make -s print-integration-features` names the
// feature and the edit that fixes it.
func TestIntegrationFeatureCoverage_ShardProblemsPrintTheFix(t *testing.T) {
	cases := []struct {
		name, old, new string
		want           []string
	}{
		{"missing", "features: audit sql", "features: audit", []string{"runs no shard for sql", "fix: add it to the features:"}},
		{"duplicate", "features: key-value", "features: key-value sql", []string{"runs sql in shard 1 and again in shard 2", "fix: delete it from shard 2"}},
		{"duplicate in one shard", "features: audit sql", "features: audit sql audit", []string{"runs audit in shard 1 and again in shard 1"}},
		{"unknown", "features: key-value", "features: key-value rate-limit", []string{`lists "rate-limit"`, "fix: delete it from shard 2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(featureJobFixture, tc.old, tc.new, 1)
			if body == featureJobFixture {
				t.Fatal("the mutation did not apply: the case tests the valid fixture")
			}
			got := strings.Join(featureJobProblems([]byte(body), featureFixtureWant), "\n")
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("problems %q do not contain %q", got, w)
				}
			}
		})
	}
}

func TestIntegrationFeatureCoverage_JobProblemsCanFail(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
		want     []string
	}{
		{name: "job missing", old: "  integration-features:\n", new: "  integration-other:\n"},
		{name: "a mapped feature in no shard", old: "features: audit sql", new: "features: audit"},
		{name: "a listed name with no mapped feature", old: "features: key-value", new: "features: key-value rate-limit"},
		{name: "a feature in two shards", old: "features: key-value", new: "features: key-value audit"},
		{name: "a feature twice in one shard", old: "features: key-value", new: "features: key-value key-value"},
		{name: "two shards of one name", old: "- shard: 2", new: "- shard: 1"},
		{name: "an empty shard", old: "features: key-value", new: "features: \"\""},
		{name: "a shard with no list", old: "\n            features: key-value", new: ""},
		{name: "a shard with no name", old: "- shard: 2\n            features", new: "- features"},
		{name: "a shard with another key", old: "features: key-value\n", new: "features: key-value\n            os: a\n"},
		{name: "a list that is an expression", old: "features: key-value", new: "features: ${{ fromJSON(needs.list.outputs.features) }}"},
		{name: "a shard that is not a mapping", old: "          - shard: 2\n            features: key-value\n", new: "          - key-value\n"},
		{name: "include is not a list", old: "        include:\n          - shard: 1\n", new: "        include: x\n        y:\n          - shard: 1\n"},
		{name: "a matrix axis", old: "        include:\n", new: "        feature: [audit]\n        include:\n"},
		{name: "no include", old: "        include:\n", new: "        shards:\n"},
		{name: "no matrix", old: "      matrix:\n        include:\n", new: "      other:\n        include:\n"},
		{name: "limit raised", old: "timeout-minutes: 120", new: "timeout-minutes: 121"},
		{name: "limit zero", old: "timeout-minutes: 120", new: "timeout-minutes: 0"},
		{name: "no limit", old: "    timeout-minutes: 120\n", new: ""},
		{name: "fail-fast default", old: "      fail-fast: false\n", new: ""},
		{name: "fail-fast true", old: "fail-fast: false", new: "fail-fast: true"},
		{name: "left the full tier", old: "    if: (github.event_name != 'pull_request' && (github.event_name != 'workflow_dispatch' || github.ref == 'refs/heads/main')) || ", new: "    if: "},
		{name: "a fixed list", old: `IT_FEATURES="$IT_FEATURES"`, new: `IT_FEATURES="cluster"`},
		{name: "the list spliced in", old: "      IT_FEATURES: ${{ matrix.features }}\n", new: ""},
		{name: "the list from elsewhere", old: "IT_FEATURES: ${{ matrix.features }}", new: "IT_FEATURES: audit"},
		{name: "on the host", old: " IT_FEATURES_IN=image", new: ""},
		{name: "one feature", old: `make integration-test-features IT_FEATURES="$IT_FEATURES" IT_FEATURES_IN=image`, new: "make integration-test IT_FEATURE=audit"},
		{name: "a second run step", old: "IT_PROJECT=\"$IT_PROJECT\" IT_PREBUILT=1\n", new: "IT_PROJECT=\"$IT_PROJECT\" IT_PREBUILT=1\n      - run: make integration-test-auth\n"},
		{name: "a make run named as the mask", old: "run: echo \"::add-mask::$GIT_CONFIG_VALUE_0\"", new: "run: make integration-test-auth"},
		{name: "no teardown", old: "      - name: Tear down (shard ${{ matrix.shard }})\n        if: always()\n        run: make docker-down-it IT_PROJECT=\"$IT_PROJECT\"\n", new: ""},
		{name: "teardown not always", old: "        if: always()\n", new: ""},
		{name: "teardown on success only", old: "if: always()", new: "if: success()"},
		{name: "teardown of the default names", old: "run: make docker-down-it IT_PROJECT=\"$IT_PROJECT\"", new: "run: make docker-down-it"},
		{name: "tests made conditional", old: "      - name: Integration tests (shard ${{ matrix.shard }})\n", new: "      - name: Integration tests (shard ${{ matrix.shard }})\n        if: always()\n"},
		{name: "project shared by every shard", old: "      IT_PROJECT: qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.shard }}\n", new: "      IT_PROJECT: qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}\n"},
		{name: "a per-shard image tag", old: "      IT_PROJECT: qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.shard }}\n", new: "      IT_PROJECT: qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.shard }}\n      IT_IMAGE: qrdb-ci-it:${{ github.run_id }}-${{ matrix.shard }}\n"},
		{name: "a job-level IT_IMAGE", old: "matrix.features }}\n    strategy:", new: "matrix.features }}\n      IT_IMAGE: ${{ needs.docker-build.outputs.it_image }}\n    strategy:"},
		{name: "docker-build not needed", old: "needs: [docker-build, arch-gates]", new: "needs: [arch-gates]"},
		{name: "no download", old: "      - name: Download the integration image\n        uses: actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0\n        with:\n          name: it-image\n          path: ${{ runner.temp }}/it-image\n", new: ""},
		{name: "the toolchain artifact", old: "          name: it-image\n", new: "          name: toolchain-image\n"},
		{name: "no load", old: "          scripts/ci-image.sh load \"$RUNNER_TEMP/it-image\" \"$IMAGE\" \"$ID\"\n", new: ""},
		{name: "a rebuild in place of the load", old: "scripts/ci-image.sh load \"$RUNNER_TEMP/it-image\" \"$IMAGE\" \"$ID\"", new: "docker image inspect \"$IMAGE\" || make docker-build-it"},
		{name: "a load with no ID check", old: "load \"$RUNNER_TEMP/it-image\" \"$IMAGE\" \"$ID\"", new: "load \"$RUNNER_TEMP/it-image\" \"$IMAGE\""},
		{name: "loaded image not handed on", old: "          echo \"IT_IMAGE=$IMAGE\" >> \"$GITHUB_ENV\"\n", new: ""},
		{name: "a per-shard image", old: "IMAGE: ${{ needs.docker-build.outputs.it_image }}", new: "IMAGE: qrdb-ci-it:${{ github.run_id }}-${{ matrix.shard }}"},
		{name: "the toolchain image", old: "outputs.it_image }}", new: "outputs.toolchain_image }}"},
		{name: "the toolchain ID", old: "outputs.it_id }}", new: "outputs.toolchain_id }}"},
		{name: "load made conditional", old: "      - name: Load the integration image\n", new: "      - name: Load the integration image\n        if: ${{ env.CI_PUBLISH == 'true' }}\n"},
		{name: "tests without IT_PREBUILT", old: " IT_PREBUILT=1\n", new: "\n"},
		{name: "a make run named as the disk cleanup", old: "run: sudo rm -rf /usr/share/dotnet", new: "run: make integration-test-auth"},
		{name: "project outside the prefix", old: "      IT_PROJECT: qrdb-ci-", new: "      IT_PROJECT: qrdb-it-"},
		{name: "project shared by every attempt", old: "qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}-", new: "qrdb-ci-${{ github.run_id }}-"},
		{name: "no names", old: "      IT_PROJECT: qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.shard }}\n", new: ""},
		{name: "no push trigger", old: "  push:\n    branches: [main]\n", new: ""},
		{name: "no pull_request trigger", old: "  pull_request:\n    types: [opened, synchronize, reopened]\n", new: ""},
		{name: "want grew", want: []string{"audit", "key-value", "sql", "watch"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, want := featureJobFixture, featureFixtureWant
			if tc.old != "" {
				body = strings.Replace(featureJobFixture, tc.old, tc.new, 1)
				if body == featureJobFixture {
					t.Fatal("the mutation did not apply: the case tests the valid fixture")
				}
			}
			if tc.want != nil {
				want = tc.want
			}
			if p := featureJobProblems([]byte(body), want); len(p) == 0 {
				t.Fatal("expected at least one problem, got none")
			}
		})
	}
}
