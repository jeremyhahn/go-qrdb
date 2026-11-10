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

// ci.yml builds the CI images once, in docker-build, and hands them to every
// job that runs a stage in them as workflow artifacts (scripts/ci-image.sh,
// DS-7377). A GitHub-hosted runner is an ephemeral VM exclusive to one job,
// so nothing one job builds is on another's daemon:
//
//   - docker-build saves each image (`scripts/ci-image.sh save`), records its
//     image ID as an output, and uploads the tar as the artifact
//     toolchain-image or it-image;
//   - every consumer downloads the artifact and loads it with
//     `scripts/ci-image.sh load`, which refuses an image whose ID is not the
//     one docker-build recorded;
//   - on a push to main alone (CI_PUBLISH), docker-build also pushes both
//     images to GHCR by digest with the run's GITHUB_TOKEN. Consumers still
//     take the artifact, so a fork's pull request, whose token cannot write
//     a package, runs the same path as main.
//
// Each property below is one edit from going false without anything else
// noticing:
//
//   - a step that logs in, pushes or pulls, or holds the registry token,
//     anywhere but docker-build's publish step: a pull request's run would
//     hold a write-scoped credential, or a consumer would run an image other
//     than the one this commit built;
//   - CI_PUBLISH widened past a push to main;
//   - a consumer that probes the daemon and rebuilds the image inline
//     (`docker image inspect || make docker-build-toolchain`): the
//     integration image upgrades its Alpine packages at build time, so a
//     rebuild of the same commit can differ from the one the other jobs ran;
//   - a consumer that runs a stage before it loads the image, loads a
//     different artifact than it downloaded, checks the image against the
//     other family's ID, reads a registry reference, or runs the integration
//     stage without IT_PREBUILT=1 (which would let make build the image);
//   - docker-build losing the save, the upload, or an output a consumer
//     loads by.
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

	// digestConsumerFloor is a discovery floor, a count beneath the jobs
	// digestConsumersWant names: a parse that finds too little fails on it
	// before any name is compared.
	digestConsumerFloor = 6

	// ciPublishIf is the publish step's condition; a step may AND further
	// terms onto ciPublishTerm.
	ciPublishIf   = "${{ env.CI_PUBLISH == 'true' }}"
	ciPublishTerm = "env.CI_PUBLISH == 'true'"

	// registryTokenExpr is the one credential the publish step may hold: the
	// run's own token, which writes the repository owner's packages.
	registryTokenExpr = "${{ secrets.GITHUB_TOKEN }}"
)

// digestConsumersWant names every ci.yml job that runs a docker-build image.
// Discovery must find each one, and each must load its image from the
// artifact, so a job the parse stops seeing fails the gate.
var digestConsumersWant = []string{
	"coverage", "coverage-fts", "coverage-merge", "fips", "image", "integration-features",
	"lint", "lint-gates", "soak", "test-clusterproof", "test-sdk", "test-tags",
}

// imageFamily is one image docker-build hands on: the artifact it travels
// in, the outputs a consumer loads it by, the registry reference only the
// publish path sets, and the stages that run in it.
type imageFamily struct {
	artifact string
	image    string
	id       string
	ref      string
	stage    *regexp.Regexp
}

var imageFamilies = map[string]imageFamily{
	"toolchain": {
		artifact: "toolchain-image", image: "toolchain_image", id: "toolchain_id", ref: "toolchain_ref",
		stage: regexp.MustCompile(`scripts/in-toolchain-image\.sh`),
	},
	"it": {
		artifact: "it-image", image: "it_image", id: "it_id", ref: "it_ref",
		stage: regexp.MustCompile(`make integration-test(-features)?\s`),
	},
}

var (
	producerOutputRef = regexp.MustCompile(`needs\.docker-build\.outputs\.([A-Za-z0-9_-]+)`)
	imageSave         = regexp.MustCompile(`scripts/ci-image\.sh save "\$[A-Za-z_][A-Za-z0-9_]*" "\$RUNNER_TEMP/([a-z-]+)"`)
	imageLoad         = regexp.MustCompile(`scripts/ci-image\.sh load "\$RUNNER_TEMP/([a-z-]+)" "\$([A-Za-z_][A-Za-z0-9_]*)" "\$([A-Za-z_][A-Za-z0-9_]*)"`)
	imagePush         = regexp.MustCompile(`scripts/ci-image\.sh push\s`)
	imageRebuild      = regexp.MustCompile(`docker image inspect|docker (buildx )?build|make (-s )?docker-build|docker-build-(toolchain|it)`)
	// registryAccess is every way a step reaches a registry with a
	// credential: the script's push, which logs in, and the docker CLI's own
	// login, push and pull.
	registryAccess = regexp.MustCompile(`scripts/ci-image\.sh push\s|docker (login|push|pull)\s|docker/login-action`)
)

const (
	uploadArtifactAction   = "actions/upload-artifact@"
	downloadArtifactAction = "actions/download-artifact@"
)

type digestStep struct {
	Name string            `yaml:"name"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
	Env  map[string]string `yaml:"env"`
}

type digestJob struct {
	If          string            `yaml:"if"`
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
	if v := strings.TrimSpace(wf.Env["CI_PUBLISH"]); v != ciPublishExpr {
		problems = append(problems, "ci.yml's CI_PUBLISH is "+strconv.Quote(v)+"; want "+strconv.Quote(ciPublishExpr)+
			": only a push to main logs in to the registry or pushes to it")
	}
	if holdsRegistryToken(wf.Env) {
		problems = append(problems, "ci.yml's workflow env holds the registry token: every step of every event would have it")
	}
	producer, ok := wf.Jobs[digestProducer]
	if !ok {
		return append(problems, "ci.yml has no "+digestProducer+" job: nothing builds the CI images"), nil
	}
	problems = append(problems, producerProblems(producer)...)

	for _, name := range slices.Sorted(maps.Keys(wf.Jobs)) {
		job := wf.Jobs[name]
		problems = append(problems, registryProblems(name, job)...)
		if name == digestProducer {
			continue
		}
		if job.Permissions["packages"] == "write" {
			problems = append(problems, name+" sets permissions packages: write: only "+digestProducer+" pushes a package")
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
			for family, f := range imageFamilies {
				if _, seen := stage[family]; !seen && f.stage.MatchString(s.Run) {
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
		problems = append(problems, digestProducer+" does not set permissions packages: write: its push to GHCR would be refused")
	}
	saved, uploaded := map[string]bool{}, map[string]bool{}
	var pushed bool
	for _, s := range job.Steps {
		for _, m := range imageSave.FindAllStringSubmatch(s.Run, -1) {
			saved[m[1]] = true
		}
		if strings.HasPrefix(s.Uses, uploadArtifactAction) {
			name := s.With["name"]
			if s.With["if-no-files-found"] != "error" {
				problems = append(problems, digestProducer+"'s upload "+strconv.Quote(name)+
					" does not set if-no-files-found: error: a missing tar would upload nothing and pass")
			}
			if !strings.HasSuffix(s.With["path"], "/"+name+"/image.tar") {
				problems = append(problems, digestProducer+"'s upload "+strconv.Quote(name)+" uploads "+
					strconv.Quote(s.With["path"])+"; want the tar scripts/ci-image.sh save wrote, <dir>/"+name+"/image.tar")
			}
			uploaded[name] = true
		}
		if imagePush.MatchString(s.Run) {
			pushed = true
			if v := strings.TrimSpace(s.Env["CI_REGISTRY_TOKEN"]); v != registryTokenExpr {
				problems = append(problems, digestProducer+"'s step "+strconv.Quote(s.Name)+" pushes with CI_REGISTRY_TOKEN "+
					strconv.Quote(v)+"; want "+registryTokenExpr+": the run's own token, never a stored credential")
			}
		}
	}
	if !pushed {
		problems = append(problems, digestProducer+" does not push with scripts/ci-image.sh push: main publishes no image")
	}
	for _, family := range slices.Sorted(maps.Keys(imageFamilies)) {
		f := imageFamilies[family]
		for _, out := range []string{f.image, f.id, f.ref} {
			if !strings.Contains(job.Outputs[out], "steps.") {
				problems = append(problems, digestProducer+" has no "+out+" output from a step: a consumer would load by nothing")
			}
		}
		if !saved[f.artifact] {
			problems = append(problems, digestProducer+" does not save the "+family+" image with "+
				"`scripts/ci-image.sh save \"$IMAGE\" \"$RUNNER_TEMP/"+f.artifact+"\"`")
		}
		if !uploaded[f.artifact] {
			problems = append(problems, digestProducer+" does not upload the artifact "+strconv.Quote(f.artifact)+
				": no consumer could load the "+family+" image")
		}
	}
	return problems
}

// registryProblems names every step of job that logs in, pushes or pulls, or
// holds the registry token, anywhere but docker-build under the publish
// condition: no consumer reaches a registry, and no event but a push to main
// does at all.
func registryProblems(name string, job digestJob) []string {
	var problems []string
	if holdsRegistryToken(job.Env) {
		problems = append(problems, name+"'s job env holds the registry token: every step of every event would have it")
	}
	for _, s := range job.Steps {
		if !registryAccess.MatchString(s.Run) && !registryAccess.MatchString(s.Uses) && !holdsRegistryToken(s.Env) {
			continue
		}
		switch {
		case name != digestProducer:
			problems = append(problems, name+"'s step "+strconv.Quote(s.Name)+" reaches a registry: a consumer takes "+
				digestProducer+"'s artifact, never a registry image, so every event runs the image this commit built")
		case !condHasTerm(s.If, ciPublishTerm):
			problems = append(problems, name+"'s step "+strconv.Quote(s.Name)+" reaches the registry under if: "+
				strconv.Quote(s.If)+"; want "+ciPublishIf+": a pull request never logs in or pushes")
		}
	}
	return problems
}

// familyOf returns the image family whose output out is, and whether out is
// the family's image, its ID or its registry reference.
func familyOf(out string) (family, kind string, ok bool) {
	for name, f := range imageFamilies {
		switch out {
		case f.image:
			return name, "image", true
		case f.id:
			return name, "id", true
		case f.ref:
			return name, "ref", true
		}
	}
	return "", "", false
}

// isImageArtifact reports whether name is the artifact of an image family.
func isImageArtifact(name string) bool {
	for _, f := range imageFamilies {
		if f.artifact == name {
			return true
		}
	}
	return false
}

// boundOutput returns the docker-build output the variable v is bound to in
// the step's env, or else the job's, and "" when it is bound to anything
// else.
func boundOutput(job digestJob, s digestStep, v string) string {
	val, ok := s.Env[v]
	if !ok {
		val = job.Env[v]
	}
	m := producerOutputRef.FindStringSubmatch(val)
	if m == nil || strings.TrimSpace(val) != "${{ needs.docker-build.outputs."+m[1]+" }}" {
		return ""
	}
	return m[1]
}

func consumerProblems(name string, job digestJob, read map[string]bool, stage map[string]int) []string {
	var problems []string
	if !slices.Contains(job.Needs, digestProducer) {
		problems = append(problems, name+" runs or reads "+digestProducer+"'s images but does not need it: "+
			"its outputs would be empty and its artifacts not yet uploaded")
	}
	families := map[string]bool{}
	for _, out := range slices.Sorted(maps.Keys(read)) {
		family, kind, ok := familyOf(out)
		switch {
		case !ok:
			problems = append(problems, name+" reads "+digestProducer+".outputs."+out+
				"; want only the image tags and IDs it loads by")
		case kind == "ref":
			problems = append(problems, name+" reads "+digestProducer+".outputs."+out+
				": a registry reference exists on main alone; a consumer loads the artifact on every event")
		default:
			families[family] = true
		}
	}
	for family := range stage {
		families[family] = true
	}
	downloaded, loaded := map[string]int{}, map[string]int{}
	for i, s := range job.Steps {
		if imageRebuild.MatchString(s.Run) {
			problems = append(problems, name+"'s step "+strconv.Quote(s.Name)+" builds or probes for an image (\""+
				firstLine(s.Run)+"\"): a consumer loads the image "+digestProducer+" built, never its own")
		}
		if art := s.With["name"]; strings.HasPrefix(s.Uses, downloadArtifactAction) && isImageArtifact(art) {
			if want := "${{ runner.temp }}/" + art; strings.TrimSpace(s.With["path"]) != want {
				problems = append(problems, name+" downloads "+strconv.Quote(art)+" to "+strconv.Quote(s.With["path"])+
					"; want "+strconv.Quote(want)+", the directory its load reads")
			}
			if _, seen := downloaded[art]; !seen {
				downloaded[art] = i
			}
		}
		for _, m := range imageLoad.FindAllStringSubmatch(s.Run, -1) {
			problems = append(problems, loadProblems(name, job, s, i, m, downloaded, loaded)...)
		}
		if imageFamilies["it"].stage.MatchString(s.Run) && !strings.Contains(s.Run, "IT_PREBUILT=1") {
			problems = append(problems, name+"'s step "+strconv.Quote(s.Name)+" runs the integration stage without "+
				"IT_PREBUILT=1: make would build the image rather than run the one it loaded")
		}
	}
	for _, family := range slices.Sorted(maps.Keys(families)) {
		at, staged := stage[family]
		if !staged {
			at = len(job.Steps)
		}
		if i, ok := loaded[family]; !ok || i > at {
			problems = append(problems, name+" has no `scripts/ci-image.sh load` of the "+family+" image before its first "+
				family+" stage: it would run an image it never took from "+digestProducer)
		}
	}
	return problems
}

// loadProblems checks one `scripts/ci-image.sh load <dir> "$IMAGE" "$ID"`
// match m at step i: both variables bound to one family's outputs, the
// directory that family's artifact, downloaded in an earlier step.
func loadProblems(name string, job digestJob, s digestStep, i int, m []string,
	downloaded, loaded map[string]int) []string {
	where := name + "'s step " + strconv.Quote(s.Name)
	imageOut, idOut := boundOutput(job, s, m[2]), boundOutput(job, s, m[3])
	family, kind, ok := familyOf(imageOut)
	if !ok || kind != "image" {
		return []string{where + " loads $" + m[2] + ", which is not bound to a " + digestProducer + " image output"}
	}
	f := imageFamilies[family]
	var problems []string
	if idOut != f.id {
		problems = append(problems, where+" checks the "+family+" image against $"+m[3]+" ("+strconv.Quote(idOut)+
			"); want "+digestProducer+".outputs."+f.id+": the load would not prove which image it ran")
	}
	if m[1] != f.artifact {
		problems = append(problems, where+" loads the "+family+" image from $RUNNER_TEMP/"+m[1]+
			"; want $RUNNER_TEMP/"+f.artifact+", where its artifact downloads")
	}
	if d, ok := downloaded[f.artifact]; !ok || d > i {
		problems = append(problems, where+" loads the "+family+" image before any download of the artifact "+
			strconv.Quote(f.artifact))
	}
	if _, seen := loaded[family]; !seen && len(problems) == 0 {
		loaded[family] = i
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

func TestCIImageDigest_EveryConsumerLoadsTheArtifact(t *testing.T) {
	problems, consumers := digestProblems(readCIWorkflowBytes(t), digestConsumerFloor)
	for _, p := range problems {
		t.Error(p)
	}
	for _, name := range digestConsumersWant {
		if !slices.Contains(consumers, name) {
			t.Errorf("%s is not a consumer of %s's images: the parse stopped seeing it, or it no longer loads one",
				name, digestProducer)
		}
	}
	t.Logf("consumers: %v", consumers)
}

// TestCIImageDigest_RevertedConsumerFails reverts one consumer of the real
// ci.yml, in memory, to the probe-and-rebuild form a self-hosted runner
// allowed, and requires the gate to name it.
func TestCIImageDigest_RevertedConsumerFails(t *testing.T) {
	body := string(readCIWorkflowBytes(t))
	const load = `scripts/ci-image.sh load "$RUNNER_TEMP/toolchain-image" "$IMAGE" "$ID"`
	const revert = `docker image inspect "$IMAGE" >/dev/null 2>&1 || make docker-build-toolchain TOOLCHAIN_IMAGE="$IMAGE"`
	if !strings.Contains(body, load) {
		t.Fatalf("ci.yml has no %q to revert: the case tests nothing", load)
	}
	problems, _ := digestProblems([]byte(strings.Replace(body, load, revert, 1)), digestConsumerFloor)
	if len(problems) == 0 {
		t.Fatal("a consumer reverted to `docker image inspect || make docker-build-toolchain` passed the gate")
	}
	t.Logf("the reverted consumer: %s", strings.Join(problems, "; "))
}

const digestFixture = `env:
  CI_PUBLISH: ${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}
jobs:
  docker-build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
    outputs:
      toolchain_image: ${{ steps.build.outputs.toolchain_image }}
      toolchain_id: ${{ steps.save.outputs.toolchain_id }}
      it_image: ${{ steps.build.outputs.it_image }}
      it_id: ${{ steps.save.outputs.it_id }}
      toolchain_ref: ${{ steps.push.outputs.toolchain_ref }}
      it_ref: ${{ steps.push.outputs.it_ref }}
    steps:
      - name: Build the images
        id: build
        run: make docker-build
      - name: Save
        id: save
        run: |
          id=$(scripts/ci-image.sh save "$TOOLCHAIN_IMAGE" "$RUNNER_TEMP/toolchain-image")
          id=$(scripts/ci-image.sh save "$IT_IMAGE" "$RUNNER_TEMP/it-image")
      - name: Upload the toolchain image
        uses: actions/upload-artifact@b7c566a772e6b6bfb58ed0dc250532a479d7789f # v6.0.0
        with:
          name: toolchain-image
          path: ${{ runner.temp }}/toolchain-image/image.tar
          retention-days: 1
          if-no-files-found: error
      - name: Upload the integration image
        uses: actions/upload-artifact@b7c566a772e6b6bfb58ed0dc250532a479d7789f # v6.0.0
        with:
          name: it-image
          path: ${{ runner.temp }}/it-image/image.tar
          if-no-files-found: error
      - name: Push
        id: push
        if: ${{ env.CI_PUBLISH == 'true' }}
        env:
          CI_REGISTRY_USER: ${{ github.actor }}
          CI_REGISTRY_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: toolchain_ref=$(scripts/ci-image.sh push "$TOOLCHAIN_IMAGE" go-qrdb-toolchain)
  lint:
    runs-on: ubuntu-latest
    needs: [preflight, docker-build]
    steps:
      - name: Download the toolchain image
        uses: actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0
        with:
          name: toolchain-image
          path: ${{ runner.temp }}/toolchain-image
      - name: Load the toolchain image
        env:
          IMAGE: ${{ needs.docker-build.outputs.toolchain_image }}
          ID: ${{ needs.docker-build.outputs.toolchain_id }}
        run: scripts/ci-image.sh load "$RUNNER_TEMP/toolchain-image" "$IMAGE" "$ID"
      - name: make lint
        run: scripts/in-toolchain-image.sh "$TOOLCHAIN_IMAGE" make lint
  legs:
    runs-on: ubuntu-latest
    needs: [docker-build]
    steps:
      - name: Download the integration image
        uses: actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0
        with:
          name: it-image
          path: ${{ runner.temp }}/it-image
      - name: Load the integration image
        env:
          IMAGE: ${{ needs.docker-build.outputs.it_image }}
          ID: ${{ needs.docker-build.outputs.it_id }}
        run: scripts/ci-image.sh load "$RUNNER_TEMP/it-image" "$IMAGE" "$ID"
      - name: Integration tests
        run: make integration-test IT_FEATURE=audit IT_PREBUILT=1
  soak:
    if: ${{ github.event_name == 'schedule' || github.event_name == 'workflow_dispatch' }}
    runs-on: ubuntu-latest
    needs: [docker-build]
    env:
      IMAGE: ${{ needs.docker-build.outputs.it_image }}
      ID: ${{ needs.docker-build.outputs.it_id }}
    steps:
      - uses: actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0
        with:
          name: it-image
          path: ${{ runner.temp }}/it-image
      - name: Load
        run: scripts/ci-image.sh load "$RUNNER_TEMP/it-image" "$IMAGE" "$ID"
      - name: Soak
        run: make integration-test IT_FEATURE=clustering IT_PREBUILT=1
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
		lintLoad = `run: scripts/ci-image.sh load "$RUNNER_TEMP/toolchain-image" "$IMAGE" "$ID"`
		legsTest = "        run: make integration-test IT_FEATURE=audit IT_PREBUILT=1\n"
	)
	for _, tc := range []struct{ name, old, new string }{
		{name: "the probe-and-rebuild form", old: lintLoad,
			new: `run: docker image inspect "$IMAGE" || make docker-build-toolchain`},
		{name: "a rebuild beside the load", old: legsTest,
			new: "        run: make docker-build-it && make integration-test IT_FEATURE=audit IT_PREBUILT=1\n"},
		{name: "a buildx build", old: legsTest,
			new: "        run: docker buildx build . && make integration-test IT_FEATURE=audit IT_PREBUILT=1\n"},
		{name: "a consumer pulls from the registry", old: legsTest,
			new: "        if: ${{ env.CI_PUBLISH == 'true' }}\n        run: docker pull \"$IMAGE\" && make integration-test IT_FEATURE=audit IT_PREBUILT=1\n"},
		{name: "a consumer logs in", old: "      - name: make lint\n",
			new: "      - name: Login\n        uses: docker/login-action@v3\n      - name: make lint\n"},
		{name: "a consumer holds the token", old: "      - name: make lint\n",
			new: "      - name: make lint\n        env:\n          GH: ${{ secrets.GITHUB_TOKEN }}\n"},
		{name: "a consumer job env holds the token", old: "  lint:\n    runs-on: ubuntu-latest\n",
			new: "  lint:\n    runs-on: ubuntu-latest\n    env:\n      CI_REGISTRY_TOKEN: x\n"},
		{name: "a consumer reads a registry reference", old: "IMAGE: ${{ needs.docker-build.outputs.it_image }}\n          ID",
			new: "IMAGE: ${{ needs.docker-build.outputs.it_ref }}\n          ID"},
		{name: "a consumer reads an unknown output", old: "      - name: make lint\n",
			new: "      - name: make lint\n        env:\n          R: ${{ needs.docker-build.outputs.runner }}\n"},
		{name: "a load of an unbound variable", old: `"$RUNNER_TEMP/it-image" "$IMAGE" "$ID"`,
			new: `"$RUNNER_TEMP/it-image" "$OTHER" "$ID"`},
		{name: "the other family's ID", old: "          ID: ${{ needs.docker-build.outputs.toolchain_id }}",
			new: "          ID: ${{ needs.docker-build.outputs.it_id }}"},
		{name: "the other family's directory", old: lintLoad,
			new: `run: scripts/ci-image.sh load "$RUNNER_TEMP/it-image" "$IMAGE" "$ID"`},
		{name: "a download to another directory", old: "          path: ${{ runner.temp }}/toolchain-image\n",
			new: "          path: ${{ runner.temp }}/elsewhere\n"},
		{name: "the wrong artifact downloaded", old: "          name: toolchain-image\n          path: ${{ runner.temp }}/toolchain-image\n",
			new: "          name: it-image\n          path: ${{ runner.temp }}/it-image\n"},
		{name: "a stage with no load", old: "      - run: make test-typescript\n",
			new: "      - run: scripts/in-toolchain-image.sh qrdb-toolchain:latest make test\n"},
		{name: "a stage before the load", old: "      - name: Download the toolchain image\n",
			new: "      - name: make vet\n        run: scripts/in-toolchain-image.sh \"$TOOLCHAIN_IMAGE\" make vet\n      - name: Download the toolchain image\n"},
		{name: "a load before the download", old: "    steps:\n      - uses: actions/download-artifact",
			new: "    steps:\n      - name: Early\n        run: scripts/ci-image.sh load \"$RUNNER_TEMP/it-image\" \"$IMAGE\" \"$ID\"\n      - uses: actions/download-artifact"},
		{name: "the integration stage without IT_PREBUILT", old: legsTest,
			new: "        run: make integration-test IT_FEATURE=audit\n"},
		{name: "docker-build not needed", old: "needs: [preflight, docker-build]", new: "needs: [preflight]"},
		{name: "a consumer writes packages", old: "  lint:\n    runs-on: ubuntu-latest\n",
			new: "  lint:\n    runs-on: ubuntu-latest\n    permissions:\n      packages: write\n"},
		{name: "no packages write", old: "packages: write", new: "packages: read"},
		{name: "no save", old: `id=$(scripts/ci-image.sh save "$IT_IMAGE" "$RUNNER_TEMP/it-image")`, new: `echo saved`},
		{name: "an upload that tolerates no file", old: "          path: ${{ runner.temp }}/it-image/image.tar\n          if-no-files-found: error\n",
			new: "          path: ${{ runner.temp }}/it-image/image.tar\n"},
		{name: "an upload of the wrong path", old: "path: ${{ runner.temp }}/it-image/image.tar",
			new: "path: ${{ runner.temp }}/it-image"},
		{name: "no upload", old: "          name: it-image\n          path: ${{ runner.temp }}/it-image/image.tar\n",
			new: "          name: integration\n          path: ${{ runner.temp }}/integration/image.tar\n"},
		{name: "an unconditional push", old: "        if: ${{ env.CI_PUBLISH == 'true' }}\n        env:\n          CI_REGISTRY_USER",
			new: "        env:\n          CI_REGISTRY_USER"},
		{name: "a push under a widened condition", old: "        if: ${{ env.CI_PUBLISH == 'true' }}\n        env:\n          CI_REGISTRY_USER",
			new: "        if: ${{ env.CI_PUBLISH == 'true' || github.event_name == 'pull_request' }}\n        env:\n          CI_REGISTRY_USER"},
		{name: "a stored credential", old: "CI_REGISTRY_TOKEN: ${{ secrets.GITHUB_TOKEN }}", new: "CI_REGISTRY_TOKEN: ${{ secrets.GHCR_PAT }}"},
		{name: "no push", old: `scripts/ci-image.sh push "$TOOLCHAIN_IMAGE"`, new: `scripts/ci-image.sh tag "$TOOLCHAIN_IMAGE"`},
		{name: "an output not from a step", old: "it_id: ${{ steps.save.outputs.it_id }}", new: "it_id: latest"},
		{name: "no ref output", old: "      it_ref: ${{ steps.push.outputs.it_ref }}\n", new: ""},
		{name: "no producer", old: "  docker-build:\n", new: "  docker-build-old:\n"},
		{name: "CI_PUBLISH on a pull request", old: "  CI_PUBLISH: ${{ github.event_name == 'push'", new: "  CI_PUBLISH: ${{ github.event_name == 'pull_request'"},
		{name: "the workflow env holds the token", old: "env:\n  CI_PUBLISH:", new: "env:\n  GH: ${{ secrets.GITHUB_TOKEN }}\n  CI_PUBLISH:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(digestFixture, tc.old) {
				t.Fatalf("the fixture has no %q: the case tests nothing", tc.old)
			}
			problems, _ := digestProblems([]byte(strings.Replace(digestFixture, tc.old, tc.new, 1)), 3)
			if len(problems) == 0 {
				t.Fatalf("%s passed the gate", tc.name)
			}
			t.Logf("%s: %s", tc.name, strings.Join(problems, "; "))
		})
	}
}
