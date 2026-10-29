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

// ci.yml takes the CI images by one of two paths, chosen by the workflow env
// CI_PUBLISH, which is true on a push to main alone (claude-agents#329):
//
//   - publish: docker-build pushes the images to the forge's registry and
//     outputs their digest references, toolchain_ref and it_ref; every other
//     job that runs a stage in an image pulls exactly that digest with
//     scripts/ci-registry.sh (DS-3516);
//   - local, on every other event: nothing logs in or pushes; each job takes
//     the image from its own runner's daemon by content-hash tag, building it
//     when missing, with scripts/ci-registry.sh local (ci-standard.md §9).
//
// Each property below is one edit from going false without anything else
// noticing:
//
//   - a step that logs in, pushes or pulls, or holds the registry token, on a
//     path other than a push to main: a pull request's run would publish or
//     hold a write-scoped credential;
//   - CI_PUBLISH widened past a push to main;
//   - a consumer that checks the daemon for an image and rebuilds it inline
//     (`docker image inspect || make docker-build-toolchain`, the form the
//     registry replaced): on main it passes on the image it built, not the
//     one docker-build pushed and the other jobs ran;
//   - a consumer that reads an output other than a digest reference, runs an
//     image it never pulled on main, or has no local path off main;
//   - a job that runs a stage in an image (scripts/in-toolchain-image.sh,
//     `make integration-test`) without being a consumer at all;
//   - a consumer placed on docker-build's runner, which the registry exists
//     to make unnecessary;
//   - docker-build handing on a ref it never validated, or losing the
//     packages permission its push needs.
//
// Every assertion is inside a loop over jobs, so a discovery floor on the
// consumers keeps a parse that finds none from passing.

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	// digestProducer is the one job that builds the CI images and, on a push
	// to main, pushes them.
	digestProducer = "docker-build"

	// digestRefPattern is the shape docker-build validates every output
	// against before a consumer starts.
	digestRefPattern = `^[^@ ]+@sha256:[0-9a-f]{64}$`

	// digestConsumerFloor is a discovery floor, a count beneath the jobs
	// digestConsumersWant names: a parse that finds too little fails on it
	// before any name is compared.
	digestConsumerFloor = 6

	// ciPublishExpr is the one value CI_PUBLISH may have: a push to main.
	ciPublishExpr = "${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}"

	// ciPublishIf and ciLocalIf are the two paths' step conditions; a step
	// may AND further terms onto either (ciPublishTerm, ciLocalTerm).
	ciPublishIf   = "${{ env.CI_PUBLISH == 'true' }}"
	ciLocalIf     = "${{ env.CI_PUBLISH != 'true' }}"
	ciPublishTerm = "env.CI_PUBLISH == 'true'"
	ciLocalTerm   = "env.CI_PUBLISH != 'true'"

	// producerRunsOn is the one runs-on the producer may have (ruling 169):
	// on the forge, a push to main (ciPublishExpr's condition, restated
	// because runs-on cannot read env) runs on the release runner, the one
	// runner holding the registry credential, and every other event on
	// bigdisk; github.com keeps ubuntu-latest.
	producerRunsOn = "${{ github.server_url == 'https://github.com' && 'ubuntu-latest' || " +
		"(github.event_name == 'push' && github.ref == 'refs/heads/main') && 'release' || 'bigdisk' }}"
)

// releaseLabel matches a runs-on that can place a job on the release
// runner: the bare label, or the label quoted in an expression.
// digestConsumersWant names every ci.yml job that runs a docker-build image,
// v1.27.0's lint-gates and five unit-suite jobs among them (ruling 211).
// Discovery must find each one, and each must take its image both ways, so a
// job the parse stops seeing, or one that loses either path, fails the gate.
var digestConsumersWant = []string{
	"coverage", "coverage-fts", "fips", "image", "integration-features", "lint",
	"lint-gates", "soak", "test-clusterproof", "test-sdk", "test-tags",
}

var releaseLabel = regexp.MustCompile(`(^|')release('|$)`)

// runnerCredential lists the names the release runner's env file exports, its
// credential contract (ruling 173, claude-agents#329), which the forge push in
// scripts/ci-registry.sh reads: an env entry under any of them, in the
// workflow, a job or a step, would mask the runner's.
var runnerCredential = []string{"RELEASE_REGISTRY", "RELEASE_REGISTRY_USER", "RELEASE_REGISTRY_TOKEN"}

// digestOutputs maps the outputs a consumer may read, the digest
// references, to the image family scripts/ci-registry.sh local builds for
// each.
var digestOutputs = map[string]string{"toolchain_ref": "toolchain", "it_ref": "it"}

var (
	producerOutputRef = regexp.MustCompile(`needs\.docker-build\.outputs\.([A-Za-z0-9_-]+)`)
	registryPull      = regexp.MustCompile(`scripts/ci-registry\.sh pull "\$([A-Za-z_][A-Za-z0-9_]*)"`)
	registryLocal     = regexp.MustCompile(`scripts/ci-registry\.sh local (toolchain|it) "\$[A-Za-z_][A-Za-z0-9_]*"`)
	imageRebuild      = regexp.MustCompile(`docker image inspect|docker (buildx )?build|make (-s )?docker-build|docker-build-(toolchain|it)`)
	// registryAccess is every way a step reaches the registry with a
	// credential: the script's push and pull, which log in, and the docker
	// CLI's own login, push and pull.
	registryAccess = regexp.MustCompile(`scripts/ci-registry\.sh (push|pull)\s|docker (login|push|pull)\s|docker/login-action`)
)

// imageStages maps each image family to the stages that run in it.
var imageStages = map[string]*regexp.Regexp{
	"toolchain": regexp.MustCompile(`scripts/in-toolchain-image\.sh`),
	"it":        regexp.MustCompile(`make integration-test(-features)?\s`),
}

type digestStep struct {
	Name string            `yaml:"name"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}

type digestJob struct {
	If          string            `yaml:"if"`
	RunsOn      string            `yaml:"runs-on"`
	Needs       []string          `yaml:"needs"`
	Permissions map[string]string `yaml:"permissions"`
	Outputs     map[string]string `yaml:"outputs"`
	Env         map[string]string `yaml:"env"`
	Steps       []digestStep      `yaml:"steps"`
}

type digestWorkflow struct {
	Env  map[string]string    `yaml:"env"`
	Jobs map[string]digestJob `yaml:"jobs"`
}

// digestProblems returns every way body departs from the properties above,
// and the consumers it found.
func digestProblems(body []byte, floor int) (problems, consumers []string) {
	var wf digestWorkflow
	if err := yaml.Unmarshal(body, &wf); err != nil {
		return []string{"ci.yml is not a workflow this gate can read: " + err.Error()}, nil
	}
	return workflowDigestProblems(wf, floor)
}

// workflowDigestProblems is digestProblems over an already parsed workflow,
// so a case can mutate one job's steps and run the same checks.
func workflowDigestProblems(wf digestWorkflow, floor int) (problems, consumers []string) {
	if v := strings.TrimSpace(wf.Env["CI_PUBLISH"]); v != ciPublishExpr {
		problems = append(problems, "ci.yml's CI_PUBLISH is "+strconv.Quote(v)+"; want "+strconv.Quote(ciPublishExpr)+
			": only a push to main logs in to the registry or pushes to it")
	}
	if holdsRegistryToken(wf.Env) {
		problems = append(problems, "ci.yml's workflow env holds the registry token: every step of every event would have it")
	}
	problems = append(problems, runnerCredentialProblems(wf)...)
	producer, ok := wf.Jobs[digestProducer]
	if !ok {
		return append(problems, "ci.yml has no "+digestProducer+" job: nothing builds the CI images"), nil
	}
	problems = append(problems, producerProblems(producer)...)

	names := make([]string, 0, len(wf.Jobs))
	for name := range wf.Jobs {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		job := wf.Jobs[name]
		problems = append(problems, publishOnlyProblems(name, job)...)
		if name == digestProducer {
			continue
		}
		if releaseLabel.MatchString(strings.TrimSpace(job.RunsOn)) {
			problems = append(problems, name+" runs on the release runner: only "+digestProducer+" does, and on a push to main alone")
		}
		read := map[string]bool{}
		for _, v := range job.Env {
			markOutputs(read, v)
		}
		stage := map[string]int{}
		for i, s := range job.Steps {
			markOutputs(read, s.Run)
			for _, v := range s.Env {
				markOutputs(read, v)
			}
			for family, re := range imageStages {
				if _, seen := stage[family]; !seen && re.MatchString(s.Run) {
					stage[family] = i
				}
			}
		}
		if len(read) == 0 && len(stage) == 0 {
			continue
		}
		consumers = append(consumers, name)
		problems = append(problems, consumerProblems(name, job, read, stage)...)
	}
	if len(consumers) < floor {
		problems = append(problems, "found "+strconv.Itoa(len(consumers))+" consumers of "+digestProducer+
			"'s images; want at least "+strconv.Itoa(floor)+": the parse found too little to check")
	}
	return problems, consumers
}

func producerProblems(job digestJob) []string {
	var problems []string
	if job.Permissions["packages"] != "write" {
		problems = append(problems, digestProducer+" does not set permissions packages: write: its push would be refused")
	}
	for out := range digestOutputs {
		if !strings.Contains(job.Outputs[out], "steps.") {
			problems = append(problems, digestProducer+" has no "+out+" output from a step: a consumer would pull nothing")
		}
	}
	if v := strings.TrimSpace(job.RunsOn); v != producerRunsOn {
		problems = append(problems, digestProducer+" runs on "+strconv.Quote(v)+"; want "+strconv.Quote(producerRunsOn)+
			": a push to main publishes from the release runner alone, and no other event runs there")
	}
	var pushed, validated bool
	for _, s := range job.Steps {
		if strings.Contains(s.Run, "scripts/ci-registry.sh push ") {
			pushed = true
		}
		if strings.Contains(s.Run, digestRefPattern) {
			validated = true
			if !condHasTerm(s.If, ciPublishTerm) {
				problems = append(problems, digestProducer+"'s step "+strconv.Quote(s.Name)+" validates the outputs under if: "+
					strconv.Quote(s.If)+"; want "+ciPublishIf+": off main there is no digest to validate")
			}
		}
	}
	if !pushed {
		problems = append(problems, digestProducer+" does not push with scripts/ci-registry.sh")
	}
	if !validated {
		problems = append(problems, digestProducer+" does not validate its outputs against "+strconv.Quote(digestRefPattern))
	}
	return problems
}

// runnerCredentialProblems holds ruling 173: no env entry in ci.yml, at the
// workflow, a job or a step, sets a name of the release runner's credential
// contract, because it would mask the value the runner's env file exports.
func runnerCredentialProblems(wf digestWorkflow) []string {
	var problems []string
	check := func(where string, env map[string]string) {
		for _, name := range runnerCredential {
			if _, ok := env[name]; ok {
				problems = append(problems, where+" sets "+name+": it masks the release runner's credential (ruling 173)")
			}
		}
	}
	check("ci.yml's workflow env", wf.Env)
	names := make([]string, 0, len(wf.Jobs))
	for name := range wf.Jobs {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		job := wf.Jobs[name]
		check(name+"'s job env", job.Env)
		for _, s := range job.Steps {
			check(name+"'s step "+strconv.Quote(s.Name), s.Env)
		}
	}
	return problems
}

// publishOnlyProblems names every step of job that logs in, pushes or
// pulls, or holds the registry token, without the publish condition: the
// assertion that no pull_request path touches the registry.
func publishOnlyProblems(name string, job digestJob) []string {
	var problems []string
	if holdsRegistryToken(job.Env) {
		problems = append(problems, name+"'s job env holds the registry token: its local path would have it too")
	}
	for _, s := range job.Steps {
		if !registryAccess.MatchString(s.Run) && !registryAccess.MatchString(s.Uses) && !holdsRegistryToken(s.Env) {
			continue
		}
		if !condHasTerm(s.If, ciPublishTerm) {
			problems = append(problems, name+"'s step "+strconv.Quote(s.Name)+" reaches the registry under if: "+
				strconv.Quote(s.If)+"; want "+ciPublishIf+": a pull request never logs in, pushes or pulls")
		}
	}
	return problems
}

func consumerProblems(name string, job digestJob, read map[string]bool, stage map[string]int) []string {
	var problems []string
	if strings.Contains(job.RunsOn, digestProducer) {
		problems = append(problems, name+"'s runs-on "+strconv.Quote(job.RunsOn)+" follows "+digestProducer+
			": a consumer takes its image on any runner and has no host affinity")
	}
	if len(read) > 0 {
		if !slices.Contains(job.Needs, digestProducer) {
			problems = append(problems, name+" reads "+digestProducer+"'s outputs but does not need it: they would be empty")
		}
		if p := job.Permissions["packages"]; p != "read" && p != "write" {
			problems = append(problems, name+" does not set permissions packages: read: its pull would be refused")
		}
	}
	families := map[string]bool{}
	for out := range read {
		family, ok := digestOutputs[out]
		if !ok {
			problems = append(problems, name+" reads "+digestProducer+".outputs."+out+
				"; want only the digest references toolchain_ref, it_ref")
			continue
		}
		families[family] = true
	}
	for family := range stage {
		families[family] = true
	}
	pulled, local := map[string]int{}, map[string]int{}
	for i, s := range job.Steps {
		if imageRebuild.MatchString(s.Run) {
			problems = append(problems, name+"'s step "+strconv.Quote(s.Name)+" builds or probes for an image (\""+
				firstLine(s.Run)+"\"): a consumer pulls docker-build's digest on main and builds through "+
				"scripts/ci-registry.sh local off it, never inline")
		}
		for _, m := range registryPull.FindAllStringSubmatch(s.Run, -1) {
			v, ok := s.Env[m[1]]
			if !ok {
				v = job.Env[m[1]]
			}
			out := producerOutputRef.FindStringSubmatch(v)
			if out == nil || strings.TrimSpace(v) != "${{ needs.docker-build.outputs."+out[1]+" }}" {
				problems = append(problems, name+" pulls $"+m[1]+", which is "+strconv.Quote(v)+
					"; want it bound to a "+digestProducer+" digest output")
				continue
			}
			if family, ok := digestOutputs[out[1]]; ok {
				if _, seen := pulled[family]; !seen {
					pulled[family] = i
				}
			}
		}
		for _, m := range registryLocal.FindAllStringSubmatch(s.Run, -1) {
			if !condHasTerm(s.If, ciLocalTerm) {
				problems = append(problems, name+"'s step "+strconv.Quote(s.Name)+" builds locally under if: "+
					strconv.Quote(s.If)+"; want "+ciLocalIf+": on main a consumer runs the digest docker-build pushed")
				continue
			}
			if _, seen := local[m[1]]; !seen {
				local[m[1]] = i
			}
		}
	}
	for _, family := range slices.Sorted(maps.Keys(families)) {
		at, staged := stage[family]
		if !staged {
			at = len(job.Steps)
		}
		if i, ok := local[family]; !ok || i > at {
			problems = append(problems, name+" has no `scripts/ci-registry.sh local "+family+"` step under "+ciLocalIf+
				" before its first "+family+" stage: off main it would run an image it never took")
		}
		i, ok := pulled[family]
		switch {
		case !ok && withoutDraftSkip(job.If) == withoutDraftSkip(nightlyIf):
			// A nightly job (nightlyIf, draft guard aside) never runs on a
			// push to main: the local path is its only one.
		case !ok:
			problems = append(problems, name+" runs the "+family+" image but never pulls it with scripts/ci-registry.sh pull: "+
				"on main it would run an image docker-build did not push")
		case i > at:
			problems = append(problems, name+" runs a "+family+" stage before it pulls the image the stage runs in")
		}
	}
	return problems
}

// condHasTerm reports whether the step condition cond requires term: term
// is the whole condition or one operand of a top-level &&, and nothing in it
// can turn the condition true without term.
func condHasTerm(cond, term string) bool {
	c := strings.TrimSpace(cond)
	c = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(c, "${{"), "}}"))
	if strings.Contains(c, "||") || strings.Contains(c, "!(") {
		return false
	}
	for _, part := range strings.Split(c, "&&") {
		if strings.TrimSpace(part) == term {
			return true
		}
	}
	return false
}

// holdsRegistryToken reports whether env carries the registry credential.
func holdsRegistryToken(env map[string]string) bool {
	if _, ok := env["CI_REGISTRY_TOKEN"]; ok {
		return true
	}
	for _, v := range env {
		if strings.Contains(v, "secrets.GITHUB_TOKEN") {
			return true
		}
	}
	return false
}

func markOutputs(read map[string]bool, s string) {
	for _, m := range producerOutputRef.FindAllStringSubmatch(s, -1) {
		read[m[1]] = true
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func readCIWorkflowBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	return b
}

func TestCIImageDigest_EveryConsumerPullsByDigest(t *testing.T) {
	problems, consumers := digestProblems(readCIWorkflowBytes(t), digestConsumerFloor)
	for _, p := range problems {
		t.Error(p)
	}
	for _, name := range digestConsumersWant {
		if !slices.Contains(consumers, name) {
			t.Errorf("%s is not a consumer of %s's images: the gate no longer checks how it takes its image", name, digestProducer)
		}
	}
	t.Logf("consumers of %s's images: %s", digestProducer, strings.Join(consumers, ", "))
}

// TestCIImageDigest_EveryNamedConsumerNeedsBothPaths drops each named
// consumer's digest pull, then its local build, from the parsed ci.yml and
// requires the gate to report that job. A nightly job has no pull to drop:
// the local path is its only one.
func TestCIImageDigest_EveryNamedConsumerNeedsBothPaths(t *testing.T) {
	paths := map[string]*regexp.Regexp{"pull": registryPull, "local": registryLocal}
	for _, name := range digestConsumersWant {
		for _, path := range slices.Sorted(maps.Keys(paths)) {
			t.Run(name+"/"+path, func(t *testing.T) {
				var wf digestWorkflow
				if err := yaml.Unmarshal(readCIWorkflowBytes(t), &wf); err != nil {
					t.Fatalf("ci.yml does not parse: %v", err)
				}
				job, ok := wf.Jobs[name]
				if !ok {
					t.Fatalf("ci.yml has no %s job", name)
				}
				kept := slices.DeleteFunc(slices.Clone(job.Steps), func(s digestStep) bool {
					return paths[path].MatchString(s.Run)
				})
				if len(kept) == len(job.Steps) {
					if path == "pull" && withoutDraftSkip(job.If) == withoutDraftSkip(nightlyIf) {
						return
					}
					t.Fatalf("%s has no %s step to drop", name, path)
				}
				job.Steps = kept
				wf.Jobs[name] = job
				problems, _ := workflowDigestProblems(wf, digestConsumerFloor)
				if !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, name+" ") }) {
					t.Fatalf("%s without its %s step passed the gate: %v", name, path, problems)
				}
			})
		}
	}
}

// TestCIImageDigest_RevertedConsumerFails reverts one consumer of the real
// ci.yml, in memory, to the probe-and-rebuild form this gate replaced, and
// requires the gate to name it.
func TestCIImageDigest_RevertedConsumerFails(t *testing.T) {
	body := string(readCIWorkflowBytes(t))
	const pull = `scripts/ci-registry.sh pull "$TOOLCHAIN_REF" "$TOOLCHAIN_HOLD"`
	const revert = `docker image inspect "$TOOLCHAIN_REF" >/dev/null 2>&1 || make docker-build-toolchain TOOLCHAIN_IMAGE="$TOOLCHAIN_REF"`
	if !strings.Contains(body, pull) {
		t.Fatalf("ci.yml has no %q to revert: the case tests nothing", pull)
	}
	problems, _ := digestProblems([]byte(strings.Replace(body, pull, revert, 1)), digestConsumerFloor)
	if len(problems) == 0 {
		t.Fatal("a consumer reverted to `docker image inspect || make docker-build-toolchain` passed the gate")
	}
	t.Logf("the reverted consumer: %s", strings.Join(problems, "; "))
}

const digestFixture = `env:
  CI_PUBLISH: ${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}
jobs:
  docker-build:
    runs-on: ${{ github.server_url == 'https://github.com' && 'ubuntu-latest' || (github.event_name == 'push' && github.ref == 'refs/heads/main') && 'release' || 'bigdisk' }}
    permissions:
      contents: read
      packages: write
    outputs:
      toolchain_ref: ${{ steps.push.outputs.toolchain_ref }}
      it_ref: ${{ steps.push.outputs.it_ref }}
    steps:
      - name: Build the images
        run: make docker-build-toolchain
      - name: Push
        id: push
        if: ${{ env.CI_PUBLISH == 'true' }}
        env:
          RUN_REGISTRY_TOKEN: ${{ github.server_url == 'https://github.com' && secrets.GITHUB_TOKEN || '' }}
        run: scripts/ci-registry.sh push "$TOOLCHAIN_IMAGE" qrdb-ci-toolchain "$TRUST"
      - name: Validate
        if: ${{ env.CI_PUBLISH == 'true' }}
        run: re='^[^@ ]+@sha256:[0-9a-f]{64}$'
  lint:
    runs-on: bigdisk
    needs: [changes, docker-build]
    permissions:
      contents: read
      packages: read
    steps:
      - name: Pull the toolchain image
        if: ${{ env.CI_PUBLISH == 'true' }}
        env:
          TOOLCHAIN_REF: ${{ needs.docker-build.outputs.toolchain_ref }}
          CI_REGISTRY_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: scripts/ci-registry.sh pull "$TOOLCHAIN_REF" "$TOOLCHAIN_HOLD"
      - name: Build or reuse the toolchain image
        if: ${{ env.CI_PUBLISH != 'true' }}
        run: image=$(scripts/ci-registry.sh local toolchain "$TOOLCHAIN_HOLD")
      - name: make lint
        run: scripts/in-toolchain-image.sh "$TOOLCHAIN_IMAGE" make lint
  legs:
    runs-on: bigdisk
    needs: [docker-build]
    permissions:
      packages: read
    steps:
      - name: Pull the integration image
        if: ${{ env.CI_PUBLISH == 'true' }}
        env:
          IT_REF: ${{ needs.docker-build.outputs.it_ref }}
          CI_REGISTRY_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: scripts/ci-registry.sh pull "$IT_REF" "$IT_HOLD"
      - name: Build or reuse the integration image
        if: ${{ env.CI_PUBLISH != 'true' }}
        run: image=$(scripts/ci-registry.sh local it "$IT_HOLD")
      - name: Integration tests
        run: make integration-test IT_FEATURE=audit
  soak:
    if: ${{ github.event_name == 'schedule' || github.event_name == 'workflow_dispatch' }}
    runs-on: bigdisk
    steps:
      - name: Build or reuse the integration image
        if: ${{ env.CI_PUBLISH != 'true' }}
        run: image=$(scripts/ci-registry.sh local it "$IT_HOLD")
      - name: Soak
        run: make integration-test IT_FEATURE=clustering
  typescript:
    runs-on: ubuntu-latest
    steps:
      - run: make test-typescript
`

func TestCIImageDigest_FixtureIsValid(t *testing.T) {
	problems, consumers := digestProblems([]byte(digestFixture), 3)
	if len(problems) != 0 {
		t.Fatalf("the valid fixture must have no problems, got %v", problems)
	}
	if !slices.Equal(consumers, []string{"legs", "lint", "soak"}) {
		t.Fatalf("consumers = %v; want [legs lint soak]", consumers)
	}
	if problems, _ := digestProblems([]byte(digestFixture), 4); len(problems) == 0 {
		t.Fatal("three consumers under a floor of four passed the gate")
	}
}

func TestCIImageDigest_CanFail(t *testing.T) {
	const (
		toolchainPullHead = "      - name: Pull the toolchain image\n        if: ${{ env.CI_PUBLISH == 'true' }}\n"
		toolchainLocal    = "        if: ${{ env.CI_PUBLISH != 'true' }}\n        run: image=$(scripts/ci-registry.sh local toolchain \"$TOOLCHAIN_HOLD\")\n"
		token             = "CI_REGISTRY_TOKEN: ${{ secrets.GITHUB_TOKEN }}"
	)
	for _, tc := range []struct{ name, old, new string }{
		{name: "the probe-and-rebuild form", old: `run: scripts/ci-registry.sh pull "$TOOLCHAIN_REF" "$TOOLCHAIN_HOLD"`,
			new: `run: docker image inspect "$TOOLCHAIN_REF" || make docker-build-toolchain`},
		{name: "a rebuild beside the pull", old: "        run: make integration-test IT_FEATURE=audit\n",
			new: "        run: make docker-build-it && make integration-test IT_FEATURE=audit\n"},
		{name: "a buildx build", old: "        run: make integration-test IT_FEATURE=audit\n",
			new: "        run: docker buildx build . && make integration-test IT_FEATURE=audit\n"},
		{name: "a tag output", old: "outputs.it_ref }}\n          CI_REGISTRY", new: "outputs.it_image }}\n          CI_REGISTRY"},
		{name: "an output read but not pulled", old: "        run: scripts/ci-registry.sh pull \"$IT_REF\" \"$IT_HOLD\"\n", new: "        run: echo \"$IT_REF\"\n"},
		{name: "a pull of an unbound variable", old: `pull "$IT_REF" "$IT_HOLD"`, new: `pull "$OTHER" "$IT_HOLD"`},
		{name: "a stage with no image output", old: "      - run: make test-typescript\n", new: "      - run: scripts/in-toolchain-image.sh qrdb-toolchain:latest make test\n"},
		{name: "a stage before the pull and the local build", old: "      - name: Pull the toolchain image\n",
			new: "      - name: make lint\n        run: scripts/in-toolchain-image.sh \"$TOOLCHAIN_IMAGE\" make lint\n      - name: Pull the toolchain image\n"},
		{name: "docker-build not needed", old: "needs: [changes, docker-build]", new: "needs: [changes]"},
		{name: "host affinity", old: "  lint:\n    runs-on: bigdisk", new: "  lint:\n    runs-on: ${{ needs.docker-build.outputs.runner }}"},
		{name: "no packages read", old: "    permissions:\n      packages: read\n", new: ""},
		{name: "no packages write", old: "packages: write", new: "packages: read"},
		{name: "no push", old: `scripts/ci-registry.sh push "$TOOLCHAIN_IMAGE"`, new: `scripts/ci-registry.sh tag "$TOOLCHAIN_IMAGE"`},
		{name: "no validation", old: `re='^[^@ ]+@sha256:[0-9a-f]{64}$'`, new: `echo done`},
		{name: "an output not from a step", old: "it_ref: ${{ steps.push.outputs.it_ref }}", new: "it_ref: qrdb-ci-it:latest"},
		{name: "no producer", old: "  docker-build:\n", new: "  docker-build-old:\n"},
		{name: "a publish off the release runner", old: "'refs/heads/main') && 'release' || 'bigdisk' }}", new: "'refs/heads/main') && 'bigdisk' || 'bigdisk' }}"},
		{name: "a publish on bigdisk", old: "  docker-build:\n    runs-on: ${{", new: "  docker-build:\n    runs-on: bigdisk\n    x: ${{"},
		{name: "the release runner on every event", old: "(github.event_name == 'push' && github.ref == 'refs/heads/main') && 'release' || 'bigdisk'", new: "'release'"},
		{name: "the release runner on a pull request", old: "(github.event_name == 'push' && github.ref", new: "(github.event_name == 'pull_request' && github.ref"},
		{name: "the release runner on github.com", old: "&& 'ubuntu-latest' ||", new: "&& 'release' ||"},
		{name: "the push step masks the runner's registry", old: "          RUN_REGISTRY_TOKEN:", new: "          RELEASE_REGISTRY: ${{ github.server_url }}\n          RUN_REGISTRY_TOKEN:"},
		{name: "the push step masks the runner's user", old: "          RUN_REGISTRY_TOKEN:", new: "          RELEASE_REGISTRY_USER: ${{ github.actor }}\n          RUN_REGISTRY_TOKEN:"},
		{name: "the push step masks the runner's token", old: "          RUN_REGISTRY_TOKEN:", new: "          RELEASE_REGISTRY_TOKEN: ${{ secrets.GITHUB_TOKEN }}\n          RUN_REGISTRY_TOKEN:"},
		{name: "a consumer step sets the runner's token", old: toolchainLocal,
			new: "        if: ${{ env.CI_PUBLISH != 'true' }}\n        env:\n          RELEASE_REGISTRY_TOKEN: x\n        run: image=$(scripts/ci-registry.sh local toolchain \"$TOOLCHAIN_HOLD\")\n"},
		{name: "a job env sets the runner's registry", old: "  legs:\n    runs-on: bigdisk\n", new: "  legs:\n    runs-on: bigdisk\n    env:\n      RELEASE_REGISTRY: forge.example\n"},
		{name: "the workflow env sets the runner's user", old: "env:\n  CI_PUBLISH", new: "env:\n  RELEASE_REGISTRY_USER: x\n  CI_PUBLISH"},
		{name: "a consumer on the release runner", old: "  lint:\n    runs-on: bigdisk", new: "  lint:\n    runs-on: release"},
		{name: "a consumer on the release runner by expression", old: "  legs:\n    runs-on: bigdisk", new: "  legs:\n    runs-on: ${{ github.ref == 'refs/heads/main' && 'release' || 'bigdisk' }}"},
		{name: "CI_PUBLISH widened to every branch", old: "${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}", new: "${{ github.ref == 'refs/heads/main' }}"},
		{name: "CI_PUBLISH widened to pull requests", old: "github.event_name == 'push' && ", new: "github.event_name != 'schedule' && "},
		{name: "no CI_PUBLISH", old: "env:\n  CI_PUBLISH: ${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}\n", new: ""},
		{name: "a push on every event", old: "        id: push\n        if: ${{ env.CI_PUBLISH == 'true' }}\n", new: "        id: push\n"},
		{name: "a push on a pull request", old: "if: ${{ env.CI_PUBLISH == 'true' }}", new: "if: ${{ env.CI_PUBLISH == 'true' || github.event_name == 'pull_request' }}"},
		{name: "validation off main", old: "      - name: Validate\n        if: ${{ env.CI_PUBLISH == 'true' }}\n", new: "      - name: Validate\n"},
		{name: "a pull on every event", old: toolchainPullHead, new: "      - name: Pull the toolchain image\n"},
		{name: "a pull under a negated condition", old: toolchainPullHead, new: "      - name: Pull the toolchain image\n        if: ${{ !(env.CI_PUBLISH == 'true') }}\n"},
		{name: "a pull under the local condition", old: toolchainPullHead, new: "      - name: Pull the toolchain image\n        if: ${{ env.CI_PUBLISH != 'true' }}\n"},
		{name: "the token in the local step", old: toolchainLocal,
			new: "        if: ${{ env.CI_PUBLISH != 'true' }}\n        env:\n          " + token + "\n        run: image=$(scripts/ci-registry.sh local toolchain \"$TOOLCHAIN_HOLD\")\n"},
		{name: "the token at job level", old: "  legs:\n    runs-on: bigdisk\n", new: "  legs:\n    runs-on: bigdisk\n    env:\n      " + token + "\n"},
		{name: "the token in the workflow env", old: "env:\n  CI_PUBLISH", new: "env:\n  " + token + "\n  CI_PUBLISH"},
		{name: "a docker login off main", old: "      - run: make test-typescript\n", new: "      - run: docker login -u x \"$REGISTRY\"\n"},
		{name: "no local build", old: "      - name: Build or reuse the toolchain image\n" + toolchainLocal, new: ""},
		{name: "a local build on main", old: toolchainLocal, new: "        run: image=$(scripts/ci-registry.sh local toolchain \"$TOOLCHAIN_HOLD\")\n"},
		{name: "a local build of the other family", old: `local toolchain "$TOOLCHAIN_HOLD"`, new: `local it "$TOOLCHAIN_HOLD"`},
		{name: "a local build after the stage", old: toolchainLocal + "      - name: make lint\n        run: scripts/in-toolchain-image.sh \"$TOOLCHAIN_IMAGE\" make lint\n",
			new: "        run: echo\n      - name: make lint\n        run: scripts/in-toolchain-image.sh \"$TOOLCHAIN_IMAGE\" make lint\n      - if: ${{ env.CI_PUBLISH != 'true' }}\n        run: image=$(scripts/ci-registry.sh local toolchain \"$TOOLCHAIN_HOLD\")\n"},
		{name: "a soak that runs on main", old: "  soak:\n    if: ${{ github.event_name == 'schedule' || github.event_name == 'workflow_dispatch' }}\n", new: "  soak:\n"},
		{name: "a soak with no local build", old: "        run: image=$(scripts/ci-registry.sh local it \"$IT_HOLD\")\n      - name: Soak\n", new: "      - name: Soak\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(digestFixture, tc.old, tc.new, 1)
			if body == digestFixture {
				t.Fatal("the mutation did not apply: the case tests the valid fixture")
			}
			if problems, _ := digestProblems([]byte(body), 3); len(problems) == 0 {
				t.Fatal("expected at least one problem, got none")
			}
		})
	}
}
