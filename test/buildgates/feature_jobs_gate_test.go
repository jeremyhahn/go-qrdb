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
// Gitea cannot expand a matrix computed at run time (handbook devops.md §1),
// so the lists are literal in ci.yml, and this gate is what keeps their
// union equal to the Makefile's. Each property below is one edit from going
// false without anything else noticing:
//
//   - a feature mapped in features.mk but in no shard: its tests never run
//     in CI, and feature_integration_coverage_test.go still passes, because
//     it checks the mapping, not the jobs;
//   - a feature in two shards, which runs twice, or a listed name whose
//     features.mk entry is empty or absent, which runs zero tests;
//   - a limit above 30 minutes, or none at all (GitHub's default is 360);
//   - fail-fast left at its default, true, so one red shard cancels the
//     others and hides whether they would have passed;
//   - a shard that runs something other than the multi-feature target, or a job
//     that leaves the full tier or stops running on push;
//   - a shard with no always() teardown, so a cancelled shard leaves its compose
//     project on the runner;
//   - a shard whose project name another shard or run shares, or that the reap
//     step's prefix does not cover;
//   - a shard that runs any image but docker-build's it_ref (#813, DS-3516):
//     every shard of a run pulls that one digest reference and holds it, never
//     rebuilds it, and its repository must sit under the reap step's prefix,
//     which is what removes it.
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
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// featureJob is ci.yml's sharded per-feature integration job.
	featureJob = "integration-features"

	// featureJobLimit is each shard's timeout-minutes ceiling: about three
	// times the ~550 s a balanced shard is expected to take (main run 65165's
	// per-feature times, docs/release/engineering.md).
	featureJobLimit = 30

	// featureShardKey and featureListKey are the two keys of each of the
	// matrix's include entries: the shard's name and its space-separated
	// feature list.
	featureShardKey = "shard"
	featureListKey  = "features"

	// featureListEnv is the job env entry that hands the shard's list to
	// featureStep through the shell, never a ${{ }} spliced into the command.
	featureListEnv = "${{ matrix." + featureListKey + " }}"

	// featureReapPrefix is the name prefix every shard's stack and image
	// carry, and the one featureReap removes stale leftovers under.
	featureReapPrefix = "qrdb-ci-"

	// featureReap is each shard's first step: it removes stacks a killed run
	// left on the runner's daemon, so they do not collide with this one.
	featureReap = "scripts/reap-stale-ci-stacks.sh " + featureReapPrefix

	// featureStep is the one test command each shard runs: every feature in
	// its list, each the feature's features.mk selection in the integration
	// image, under the shard's project, past a failing one. The image is the
	// job's IT_IMAGE, featureImageRef, which the Makefile reads from the
	// environment.
	featureStep = `make integration-test-features IT_FEATURES="$IT_FEATURES" IT_FEATURES_IN=image IT_PROJECT="$IT_PROJECT"`

	// featureTeardown is the shard's last step, run under always(): it removes
	// the compose project featureStep ran under, so a cancelled shard leaves no
	// stack on the runner. The image stays for the other legs; the reap step
	// removes it once no run can need it.
	featureTeardown = `make docker-down-it IT_PROJECT="$IT_PROJECT"`

	// featureImageRepo is the repository of `make -s it-image`: under
	// featureReapPrefix, so the reap step removes old integration images.
	featureImageRepo = featureReapPrefix + "it"

	// featureImageRef is the pull step's IT_REF on a push to main (DS-3516):
	// the digest reference docker-build pushed, one image for every leg of
	// the run.
	featureImageRef = "${{ needs.docker-build.outputs.it_ref }}"

	// featurePull is the leg's step after the reap on a push to main alone
	// (ciPublishIf): it pulls featureImageRef by digest, holds it, hands it
	// to the tests as IT_IMAGE and never builds (DS-3516).
	featurePull = "scripts/ci-registry.sh pull \"$IT_REF\" \"$IT_HOLD\"\necho \"IT_IMAGE=$IT_REF\" >> \"$GITHUB_ENV\""

	// featureLocal is featurePull's counterpart on every other event
	// (ciLocalIf, claude-agents#329): it takes the integration image from
	// the runner's own daemon by content-hash tag, building it when missing,
	// holds it and hands the tag on. It never logs in.
	featureLocal = "image=$(scripts/ci-registry.sh local it \"$IT_HOLD\")\necho \"IT_IMAGE=$image\" >> \"$GITHUB_ENV\""

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
		TimeoutMinutes *int              `yaml:"timeout-minutes"`
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
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
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
	case *tm <= 0 || *tm > featureJobLimit:
		problems = append(problems, featureJob+"'s timeout-minutes is "+strconv.Itoa(*tm)+
			"; want 1.."+strconv.Itoa(featureJobLimit)+": about three times a shard's expected time")
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
	for _, s := range job.Steps {
		// The credential mask (ci-template v1.21.1) leads the shard; its shape is
		// ci_logs_test.go's to check. Only a first run step that runs no make
		// target is taken for it, so the name cannot carry a narrowed run past
		// this gate.
		if len(runs) == 0 && s.Name == maskStepName && !strings.Contains(s.Run, "make ") {
			continue
		}
		if s.Run != "" {
			runs = append(runs, strings.TrimSpace(s.Run))
			conds = append(conds, strings.TrimSpace(s.If))
		}
	}
	if !slices.Equal(runs, []string{featureReap, featurePull, featureLocal, featureStep, featureTeardown}) {
		problems = append(problems, featureJob+" runs "+strconv.Quote(strings.Join(runs, "; "))+
			"; want exactly `"+featureReap+"`, `"+featurePull+"`, `"+featureLocal+"`, `"+featureStep+
			"`, then `"+featureTeardown+"`")
	} else if !slices.Equal(conds, []string{"", ciPublishIf, ciLocalIf, "", "always()"}) {
		problems = append(problems, featureJob+"'s steps run under if: "+strconv.Quote(strings.Join(conds, ", "))+
			"; want the reap and the tests unconditional, the pull under "+ciPublishIf+
			", the local build under "+ciLocalIf+" and the teardown under always(), "+
			"so exactly one of the two takes the image and a failed or cancelled shard still removes its stack and its hold")
	}
	for _, s := range job.Steps {
		if strings.TrimSpace(s.Run) == featurePull && s.Env["IT_REF"] != featureImageRef {
			problems = append(problems, featureJob+"'s pull step sets IT_REF "+strconv.Quote(s.Env["IT_REF"])+
				"; want "+strconv.Quote(featureImageRef)+": every shard runs the one image docker-build "+
				"pushed, by digest, and a shard-chosen name would pull another")
		}
	}
	// The project the steps pass must be this shard's alone: shards of one run,
	// and runs sharing a daemon, would otherwise share a stack.
	v := job.Env["IT_PROJECT"]
	if !strings.HasPrefix(v, featureReapPrefix) {
		problems = append(problems, featureJob+"'s IT_PROJECT is "+strconv.Quote(v)+
			"; want it under "+strconv.Quote(featureReapPrefix)+", the prefix the reap step removes")
	}
	for _, part := range []string{"${{ github.run_id }}", "${{ github.run_attempt }}", "${{ matrix." + featureShardKey + " }}"} {
		if !strings.Contains(v, part) {
			problems = append(problems, featureJob+"'s IT_PROJECT is "+strconv.Quote(v)+
				"; want it to carry "+part+", so no other shard or run shares it")
		}
	}
	// The image is the opposite: one image for every shard of the run (#813).
	// The pull or the local build sets IT_IMAGE through GITHUB_ENV; a job-level
	// value would be empty off main and, being defined, would override the
	// Makefile's content-hash default.
	if img, set := job.Env["IT_IMAGE"]; set {
		problems = append(problems, featureJob+" sets IT_IMAGE "+strconv.Quote(img)+
			" at job level; want it set by the pull or the local build step alone, "+
			"which know the image this shard holds")
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
			cmd := exec.Command("make", "-s", "docker-build-it", "IT_IMAGE=qrdb-ci-it:test", "IT_HOLD=", "MODULE_ENV_FILE="+envFile) // #nosec G204 -- a fixed target and a tempdir path
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

// holdDocker is a stub docker that keeps its images and containers as files
// under $STUB_DIR (an image file holds its .Created and its
// .Metadata.LastTagTime.Unix, the reaper's inspect format; a container file
// its image, then its .Created). An image file may carry a second line, the
// image ID: names with one ID are one image, and a name without the line is an
// image of its own. As dockerd does, `image rm` of one of several names only
// untags it, and of an image's last name refuses when any container, in any
// state, uses the image; `ps -aq --filter ancestor=<name>` lists those
// containers. buildx builds and tags
// $STUB_IMAGE, so a build sets both times to now. A
// $STUB_DIR/vanish file makes the next `create` find the image gone, as a
// reaper running between docker-build-it's check and its hold would. A
// registry-qualified name (<registry>/<owner>/<repo>:<tag> or
// ...@sha256:<hex>) is a file in subdirectories, listed by `images --digests`
// as the reaper's registry loop reads it.
const holdDocker = `#!/bin/sh
img=$STUB_DIR/images ctr=$STUB_DIR/containers
mkdir -p "$img" "$ctr"
# id_of prints the image a name refers to: the second line of its file, or
# the name itself, so a name seeded alone is an image of its own.
id_of() {
	id=
	[ -f "$img/$1" ] && { read -r _; read -r id; } < "$img/$1"
	echo "${id:-$1}"
}
# ctr_id prints the image container file $1 was created from.
ctr_id() {
	used= id=
	{ read -r used; read -r _; read -r id; } < "$1"
	[ -n "$id" ] || id=$(id_of "$used")
	echo "$id"
}
case "$1 $2" in
"buildx version") exit 0 ;;
"buildx build") : > "$STUB_DIR/built"; printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(date +%s)" > "$img/$STUB_IMAGE"; exit 0 ;;
"image inspect")
	eval ref=\${$#}
	[ -f "$img/$ref" ] || exit 1
	[ "$3" = -f ] && cat "$img/$ref"
	exit 0 ;;
"image rm")
	# Docker untags one of several names whether or not a container uses
	# the image, and refuses only the last name of an image in use.
	[ -f "$img/$3" ] || exit 1
	id=$(id_of "$3")
	for f in $(find "$img" -type f); do
		ref=${f#"$img"/}
		[ "$ref" != "$3" ] && [ "$(id_of "$ref")" = "$id" ] && { rm -f "$img/$3"; exit 0; }
	done
	for c in "$ctr"/*; do
		[ -f "$c" ] || continue
		[ "$(ctr_id "$c")" = "$id" ] && { echo "conflict: unable to remove $3: image is being used by a container" >&2; exit 1; }
	done
	rm -f "$img/$3"; exit 0 ;;
"images --filter") for i in "$img"/*; do [ -f "$i" ] && basename "$i"; done; exit 0 ;;
"images --digests")
	find "$img" -mindepth 2 -type f | while read -r f; do
		ref=${f#"$img"/}
		case $ref in
		*@*) printf '%s <none> %s\n' "${ref%@*}" "${ref#*@}" ;;
		*) printf '%s %s <none>\n' "${ref%:*}" "${ref##*:}" ;;
		esac
	done; exit 0 ;;
"container inspect")
	eval name=\${$#}
	[ -f "$ctr/$name" ] || exit 1
	{ read -r used; } < "$ctr/$name"; echo "$used"; exit 0 ;;
"create --pull")
	name= ref=
	while [ $# -gt 0 ]; do case $1 in --name) name=$2; shift ;; *) ref=$1 ;; esac; shift; done
	[ -f "$STUB_DIR/vanish" ] && rm -f "$STUB_DIR/vanish" "$img/$ref"
	[ -f "$img/$ref" ] || { echo "No such image: $ref" >&2; exit 1; }
	printf '%s\n%s\n%s\n' "$ref" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(id_of "$ref")" > "$ctr/$name"; exit 0 ;;
"ps -aq")
	case $4 in ancestor=*) ;; *) exit 99 ;; esac
	id=$(id_of "${4#ancestor=}")
	for c in "$ctr"/*; do [ -f "$c" ] && [ "$(ctr_id "$c")" = "$id" ] && echo "${c##*/}"; done
	exit 0 ;;
"rm -f" | "rm -v")
	shift 2
	for name; do [ -f "$ctr/$name" ] || exit 1; rm -f "$ctr/$name"; done; exit 0 ;;
"ps -a")
	case $4 in name=*) for c in "$ctr"/*; do [ -f "$c" ] && echo "${c##*/} ${c##*/} "; done ;; esac
	exit 0 ;;
"inspect -f")
	[ -f "$ctr/$4" ] || exit 1
	{ read -r used; read -r created; } < "$ctr/$4"
	echo "created 0001-01-01T00:00:00Z $created"; exit 0 ;;
"network ls" | "volume ls") exit 0 ;;
esac
echo "stub docker: unexpected $*" >&2
exit 99
`

// neverTagged is .Metadata.LastTagTime.Unix of an image never tagged since it
// was created: Go's zero time.
const neverTagged = "-62135596800"

// holdStub writes holdDocker to a new directory holding image, created at
// created and last tagged at tagged (Unix seconds), and returns the directory.
func holdStub(t *testing.T, image, created, tagged string) string {
	t.Helper()
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte(holdDocker), 0o700); err != nil { // #nosec G306 -- the stub must be executable
		t.Fatalf("write stub docker: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(stub, "images"), 0o700); err != nil {
		t.Fatalf("mkdir stub images: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stub, "images", image), []byte(created+" "+tagged+"\n"), 0o600); err != nil {
		t.Fatalf("seed stub image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stub, "module.env"), []byte("GIT_CONFIG_KEY_0=stub\n"), 0o600); err != nil {
		t.Fatalf("write module env: %v", err)
	}
	return stub
}

// runHoldStub runs name with args in root against the stub docker in stub,
// with the reaper's image age at zero so that only a hold keeps an image, and
// returns the combined output.
func runHoldStub(t *testing.T, root, stub, image, name string, args ...string) string {
	t.Helper()
	return runHoldStubAged(t, root, stub, image, "0", name, args...)
}

// runHoldStubAged is runHoldStub with the reaper's image age at minAge
// seconds (REAP_IMAGE_MIN_AGE).
func runHoldStubAged(t *testing.T, root, stub, image, minAge, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...) // #nosec G204 -- this file's own make targets and script
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_DIR="+stub, "STUB_IMAGE="+image, "REAP_IMAGE_MIN_AGE="+minAge)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

// TestIntegrationFeatureCoverage_HeldImageSurvivesTheReaper is run 57540: the
// image job resolved a cached qrdb-ci-it image whose .Created was hours old,
// and another job's reaper removed it before the job's `docker save`. The
// hold docker-build-it takes must keep the image from every reaper, whatever
// its age, and releasing the hold must let the reaper remove it again.
func TestIntegrationFeatureCoverage_HeldImageSurvivesTheReaper(t *testing.T) {
	root := repoRoot(t)
	const image, hold = "qrdb-ci-it:test", "qrdb-ci-hold-1-1-image"
	for _, tc := range []struct {
		name      string
		vanish    bool
		wantBuild bool
	}{
		{name: "a cached image older than any run", vanish: false, wantBuild: false},
		{name: "the image reaped between the check and the hold", vanish: true, wantBuild: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := holdStub(t, image, "2000-01-01T00:00:00Z", neverTagged)
			if tc.vanish {
				if err := os.WriteFile(filepath.Join(stub, "vanish"), nil, 0o600); err != nil {
					t.Fatalf("write vanish: %v", err)
				}
			}
			imageFile := filepath.Join(stub, "images", image)
			runHoldStub(t, root, stub, image, "make", "-s", "docker-build-it",
				"IT_IMAGE="+image, "IT_HOLD="+hold, "MODULE_ENV_FILE="+filepath.Join(stub, "module.env"))
			if _, err := os.Stat(filepath.Join(stub, "built")); (err == nil) != tc.wantBuild {
				t.Fatalf("make docker-build-it built the image: %v; want %v", err == nil, tc.wantBuild)
			}
			reaped := runHoldStub(t, root, stub, image, "scripts/reap-stale-ci-stacks.sh", "qrdb-ci-")
			if _, err := os.Stat(imageFile); err != nil {
				t.Fatalf("the reaper removed %s while %s held it:\n%s", image, hold, reaped)
			}
			runHoldStub(t, root, stub, image, "make", "-s", "docker-release-it", "IT_HOLD="+hold)
			if _, err := os.Stat(filepath.Join(stub, "containers", hold)); err == nil {
				t.Fatalf("make docker-release-it left %s", hold)
			}
			reaped = runHoldStub(t, root, stub, image, "scripts/reap-stale-ci-stacks.sh", "qrdb-ci-")
			if _, err := os.Stat(imageFile); err == nil {
				t.Fatalf("the reaper kept %s after its hold was released:\n%s", image, reaped)
			}
		})
	}
}

// TestIntegrationFeatureCoverage_PulledImagesAreReaped pins the reaper's
// registry loop (DS-3516): a CI image pulled by digest, or tagged by the
// push, is reaped by the same age rule as a local one, never while a job's
// hold uses it, and an image of another repository is not touched.
func TestIntegrationFeatureCoverage_PulledImagesAreReaped(t *testing.T) {
	root := repoRoot(t)
	const reg, old = "db.example.com/jeremyhahn/", "2000-01-01T00:00:00Z"
	digest := "@sha256:" + strings.Repeat("a", 64)
	now := time.Now().UTC()
	held := reg + featureImageRepo + digest
	images := []struct {
		ref, tagged string
		wantKept    bool
	}{
		{ref: held, tagged: neverTagged, wantKept: true},
		{ref: reg + featureReapPrefix + "toolchain" + digest, tagged: neverTagged, wantKept: false},
		{ref: reg + featureImageRepo + ":0123456789abcdef-pr", tagged: neverTagged, wantKept: false},
		{ref: reg + featureReapPrefix + "toolchain:fedcba9876543210-main", tagged: strconv.FormatInt(now.Unix(), 10), wantKept: true},
		{ref: reg + "other-it" + digest, tagged: neverTagged, wantKept: true},
	}
	stub := holdStub(t, featureImageRepo+":local", old, neverTagged)
	for _, im := range images {
		f := filepath.Join(stub, "images", filepath.FromSlash(im.ref))
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatalf("mkdir stub image: %v", err)
		}
		if err := os.WriteFile(f, []byte(old+" "+im.tagged+"\n"), 0o600); err != nil {
			t.Fatalf("seed stub image: %v", err)
		}
	}
	ctr := filepath.Join(stub, "containers")
	if err := os.MkdirAll(ctr, 0o700); err != nil {
		t.Fatalf("mkdir stub containers: %v", err)
	}
	hold := filepath.Join(ctr, "qrdb-ci-hold-1-1-key-value")
	if err := os.WriteFile(hold, []byte(held+"\n"+now.Format(time.RFC3339)+"\n"), 0o600); err != nil {
		t.Fatalf("seed stub hold: %v", err)
	}
	reaped := runHoldStubAged(t, root, stub, featureImageRepo+":local", "3600", "scripts/reap-stale-ci-stacks.sh", featureReapPrefix)
	for _, im := range images {
		_, err := os.Stat(filepath.Join(stub, "images", filepath.FromSlash(im.ref)))
		if (err == nil) != im.wantKept {
			t.Fatalf("%s kept: %v; want %v:\n%s", im.ref, err == nil, im.wantKept, reaped)
		}
	}
	if _, err := os.Stat(hold); err != nil {
		t.Fatalf("the reaper removed a live job's hold:\n%s", reaped)
	}
}

// TestIntegrationFeatureCoverage_HeldImageKeepsEveryName: an image known by
// several names (the producer's local content tag, its registry tag and the
// digest reference a consumer pulled) and held by one job keeps every name.
// Docker untags any but the last name of an image in use without complaint,
// so a reaper that relied on its refusal dropped the digest reference the
// holder's next `docker create --pull never` runs. Released, every name goes.
func TestIntegrationFeatureCoverage_HeldImageKeepsEveryName(t *testing.T) {
	root := repoRoot(t)
	const reg, old, id = "db.example.com/jeremyhahn/", "2000-01-01T00:00:00Z", "sha256:image"
	local := featureImageRepo + ":0123456789abcdef"
	digestRef := reg + featureImageRepo + "@sha256:" + strings.Repeat("b", 64)
	names := []string{local, reg + featureImageRepo + ":0123456789abcdef-pr", digestRef}
	for _, held := range []bool{true, false} {
		t.Run(map[bool]string{true: "held", false: "released"}[held], func(t *testing.T) {
			stub := holdStub(t, local, old, neverTagged)
			for _, n := range names {
				f := filepath.Join(stub, "images", filepath.FromSlash(n))
				if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
					t.Fatalf("mkdir stub image: %v", err)
				}
				if err := os.WriteFile(f, []byte(old+" "+neverTagged+"\n"+id+"\n"), 0o600); err != nil {
					t.Fatalf("seed stub image: %v", err)
				}
			}
			ctr := filepath.Join(stub, "containers")
			if err := os.MkdirAll(ctr, 0o700); err != nil {
				t.Fatalf("mkdir stub containers: %v", err)
			}
			if held {
				hold := filepath.Join(ctr, "qrdb-ci-hold-1-1-key-value")
				body := digestRef + "\n" + time.Now().UTC().Format(time.RFC3339) + "\n" + id + "\n"
				if err := os.WriteFile(hold, []byte(body), 0o600); err != nil {
					t.Fatalf("seed stub hold: %v", err)
				}
			}
			reaped := runHoldStub(t, root, stub, local, "scripts/reap-stale-ci-stacks.sh", featureReapPrefix)
			for _, n := range names {
				if _, err := os.Stat(filepath.Join(stub, "images", filepath.FromSlash(n))); (err == nil) != held {
					t.Fatalf("%s kept: %v; want %v:\n%s", n, err == nil, held, reaped)
				}
			}
		})
	}
}

// TestIntegrationFeatureCoverage_DeadJobsHoldIsReaped: a job that died before
// its teardown leaves its hold behind; the reaper removes the stale hold and
// then, in the same pass, the image, so the disk stays bounded. A live job's
// hold, just created, keeps both.
func TestIntegrationFeatureCoverage_DeadJobsHoldIsReaped(t *testing.T) {
	root := repoRoot(t)
	const image, hold = "qrdb-ci-it:test", "qrdb-ci-hold-1-1-auth"
	for _, tc := range []struct {
		name     string
		created  string
		wantKept bool
	}{
		{name: "dead job", created: "2000-01-01T00:00:00Z", wantKept: false},
		{name: "live job", created: time.Now().UTC().Format(time.RFC3339), wantKept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := holdStub(t, image, "2000-01-01T00:00:00Z", neverTagged)
			if err := os.MkdirAll(filepath.Join(stub, "containers"), 0o700); err != nil {
				t.Fatalf("mkdir stub containers: %v", err)
			}
			if err := os.WriteFile(filepath.Join(stub, "containers", hold), []byte(image+"\n"+tc.created+"\n"), 0o600); err != nil {
				t.Fatalf("seed hold: %v", err)
			}
			reaped := runHoldStub(t, root, stub, image, "scripts/reap-stale-ci-stacks.sh", "qrdb-ci-")
			for _, f := range []string{filepath.Join(stub, "containers", hold), filepath.Join(stub, "images", image)} {
				if _, err := os.Stat(f); (err == nil) != tc.wantKept {
					t.Fatalf("%s kept: %v; want %v:\n%s", filepath.Base(f), err == nil, tc.wantKept, reaped)
				}
			}
		})
	}
}

// TestIntegrationFeatureCoverage_RetaggedImageAgesFromItsTag is ci-template
// v1.23.1 (reaper v3; go-quicraft main runs 57389 and 57484): a rebuild from a
// full cache keeps an image's old .Created but tags it again, so the reaper
// ages an unheld image from the later of the two. An image re-tagged within
// REAP_IMAGE_MIN_AGE stays; one neither created nor tagged within it goes,
// and so does one never tagged since an old build.
func TestIntegrationFeatureCoverage_RetaggedImageAgesFromItsTag(t *testing.T) {
	root := repoRoot(t)
	const image = "qrdb-ci-it:test"
	for _, tc := range []struct {
		name     string
		tagged   string
		wantKept bool
	}{
		{name: "re-tagged a minute ago", tagged: strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10), wantKept: true},
		{name: "re-tagged long ago", tagged: strconv.FormatInt(time.Now().Add(-3*time.Hour).Unix(), 10), wantKept: false},
		{name: "never tagged since an old build", tagged: neverTagged, wantKept: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := holdStub(t, image, "2000-01-01T00:00:00Z", tc.tagged)
			reaped := runHoldStubAged(t, root, stub, image, "7200", "scripts/reap-stale-ci-stacks.sh", "qrdb-ci-")
			if _, err := os.Stat(filepath.Join(stub, "images", image)); (err == nil) != tc.wantKept {
				t.Fatalf("%s kept: %v; want %v:\n%s", image, err == nil, tc.wantKept, reaped)
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
    if: github.event_name != 'pull_request' || contains(github.event.pull_request.labels.*.name, 'ci:full')
    needs: [docker-build, arch-gates]
    timeout-minutes: 30
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
      - name: Reap stale CI stacks
        run: scripts/reap-stale-ci-stacks.sh qrdb-ci-
      - name: Pull the integration image
        if: ${{ env.CI_PUBLISH == 'true' }}
        env:
          IT_REF: ${{ needs.docker-build.outputs.it_ref }}
        run: |
          scripts/ci-registry.sh pull "$IT_REF" "$IT_HOLD"
          echo "IT_IMAGE=$IT_REF" >> "$GITHUB_ENV"
      - name: Build or reuse the integration image on this runner
        if: ${{ env.CI_PUBLISH != 'true' }}
        run: |
          image=$(scripts/ci-registry.sh local it "$IT_HOLD")
          echo "IT_IMAGE=$image" >> "$GITHUB_ENV"
      - name: Integration tests (shard ${{ matrix.shard }})
        run: make integration-test-features IT_FEATURES="$IT_FEATURES" IT_FEATURES_IN=image IT_PROJECT="$IT_PROJECT"
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
		{name: "limit raised", old: "timeout-minutes: 30", new: "timeout-minutes: 31"},
		{name: "limit zero", old: "timeout-minutes: 30", new: "timeout-minutes: 0"},
		{name: "no limit", old: "    timeout-minutes: 30\n", new: ""},
		{name: "fail-fast default", old: "      fail-fast: false\n", new: ""},
		{name: "fail-fast true", old: "fail-fast: false", new: "fail-fast: true"},
		{name: "left the full tier", old: "    if: github.event_name != 'pull_request' || ", new: "    if: "},
		{name: "a fixed list", old: `IT_FEATURES="$IT_FEATURES"`, new: `IT_FEATURES="cluster"`},
		{name: "the list spliced in", old: "      IT_FEATURES: ${{ matrix.features }}\n", new: ""},
		{name: "the list from elsewhere", old: "IT_FEATURES: ${{ matrix.features }}", new: "IT_FEATURES: audit"},
		{name: "on the host", old: " IT_FEATURES_IN=image", new: ""},
		{name: "one feature", old: `make integration-test-features IT_FEATURES="$IT_FEATURES" IT_FEATURES_IN=image`, new: "make integration-test IT_FEATURE=audit"},
		{name: "a second run step", old: "IT_FEATURES_IN=image IT_PROJECT=\"$IT_PROJECT\"\n", new: "IT_FEATURES_IN=image IT_PROJECT=\"$IT_PROJECT\"\n      - run: make integration-test-auth\n"},
		{name: "a make run named as the mask", old: "run: echo \"::add-mask::$GIT_CONFIG_VALUE_0\"", new: "run: make integration-test-auth"},
		{name: "no reap", old: "      - name: Reap stale CI stacks\n        run: scripts/reap-stale-ci-stacks.sh qrdb-ci-\n", new: ""},
		{name: "reap of another prefix", old: "reap-stale-ci-stacks.sh qrdb-ci-", new: "reap-stale-ci-stacks.sh qrdb-"},
		{name: "no teardown", old: "      - name: Tear down (shard ${{ matrix.shard }})\n        if: always()\n        run: make docker-down-it IT_PROJECT=\"$IT_PROJECT\"\n", new: ""},
		{name: "teardown not always", old: "        if: always()\n", new: ""},
		{name: "teardown on success only", old: "if: always()", new: "if: success()"},
		{name: "teardown of the default names", old: "run: make docker-down-it IT_PROJECT=\"$IT_PROJECT\"", new: "run: make docker-down-it"},
		{name: "tests made conditional", old: "      - name: Integration tests (shard ${{ matrix.shard }})\n", new: "      - name: Integration tests (shard ${{ matrix.shard }})\n        if: always()\n"},
		{name: "project shared by every shard", old: "      IT_PROJECT: qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.shard }}\n", new: "      IT_PROJECT: qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}\n"},
		{name: "a per-shard image tag", old: "      IT_PROJECT: qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.shard }}\n", new: "      IT_PROJECT: qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.shard }}\n      IT_IMAGE: qrdb-ci-it:${{ github.run_id }}-${{ matrix.shard }}\n"},
		{name: "a per-shard IT_REF", old: "IT_REF: ${{ needs.docker-build.outputs.it_ref }}", new: "IT_REF: qrdb-ci-it:${{ github.run_id }}-${{ matrix.shard }}"},
		{name: "the toolchain image", old: "outputs.it_ref }}", new: "outputs.toolchain_ref }}"},
		{name: "no image", old: "        env:\n          IT_REF: ${{ needs.docker-build.outputs.it_ref }}\n", new: ""},
		{name: "a job-level IT_IMAGE", old: "matrix.features }}\n    strategy:", new: "matrix.features }}\n      IT_IMAGE: ${{ needs.docker-build.outputs.it_ref }}\n    strategy:"},
		{name: "docker-build not needed", old: "needs: [docker-build, arch-gates]", new: "needs: [arch-gates]"},
		{name: "no pull", old: "      - name: Pull the integration image\n        if: ${{ env.CI_PUBLISH == 'true' }}\n        env:\n          IT_REF: ${{ needs.docker-build.outputs.it_ref }}\n        run: |\n          scripts/ci-registry.sh pull \"$IT_REF\" \"$IT_HOLD\"\n          echo \"IT_IMAGE=$IT_REF\" >> \"$GITHUB_ENV\"\n", new: ""},
		{name: "a rebuild in place of the pull", old: "scripts/ci-registry.sh pull \"$IT_REF\" \"$IT_HOLD\"", new: "docker image inspect \"$IT_REF\" || make docker-build-it"},
		{name: "a pull with no hold", old: "pull \"$IT_REF\" \"$IT_HOLD\"", new: "pull \"$IT_REF\""},
		{name: "pull on every event", old: "        if: ${{ env.CI_PUBLISH == 'true' }}\n", new: ""},
		{name: "pull made unconditional", old: "if: ${{ env.CI_PUBLISH == 'true' }}", new: "if: always()"},
		{name: "pull widened past main", old: "if: ${{ env.CI_PUBLISH == 'true' }}", new: "if: ${{ env.CI_PUBLISH == 'true' || github.event_name == 'pull_request' }}"},
		{name: "pulled image not handed on", old: "          echo \"IT_IMAGE=$IT_REF\" >> \"$GITHUB_ENV\"\n", new: ""},
		{name: "no local build", old: "      - name: Build or reuse the integration image on this runner\n        if: ${{ env.CI_PUBLISH != 'true' }}\n        run: |\n          image=$(scripts/ci-registry.sh local it \"$IT_HOLD\")\n          echo \"IT_IMAGE=$image\" >> \"$GITHUB_ENV\"\n", new: ""},
		{name: "local build on main too", old: "        if: ${{ env.CI_PUBLISH != 'true' }}\n", new: ""},
		{name: "local build under publish", old: "if: ${{ env.CI_PUBLISH != 'true' }}", new: "if: ${{ env.CI_PUBLISH == 'true' }}"},
		{name: "local build of the toolchain", old: "local it \"$IT_HOLD\"", new: "local toolchain \"$IT_HOLD\""},
		{name: "local build with no hold", old: "local it \"$IT_HOLD\"", new: "local it"},
		{name: "local image not handed on", old: "          echo \"IT_IMAGE=$image\" >> \"$GITHUB_ENV\"\n", new: ""},
		{name: "local image read through a masked substitution", old: "          image=$(scripts/ci-registry.sh local it \"$IT_HOLD\")\n          echo \"IT_IMAGE=$image\" >> \"$GITHUB_ENV\"\n", new: "          echo \"IT_IMAGE=$(scripts/ci-registry.sh local it \"$IT_HOLD\")\" >> \"$GITHUB_ENV\"\n"},
		{name: "project shared by every attempt", old: "qrdb-ci-${{ github.run_id }}-${{ github.run_attempt }}-", new: "qrdb-ci-${{ github.run_id }}-"},
		{name: "project outside the reaped prefix", old: "      IT_PROJECT: qrdb-ci-", new: "      IT_PROJECT: qrdb-it-"},
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
