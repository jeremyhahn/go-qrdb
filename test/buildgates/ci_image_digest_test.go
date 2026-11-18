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
	// digestProducer is the job that builds the CI images on a GitHub-hosted
	// runner, hands them to the hosted legs as artifacts and, on a push to
	// main, pushes them to GHCR.
	digestProducer = "docker-build"

	// poolProducer builds the same images on the Docker pool, from the host's
	// BuildKit cache and through the LAN Docker Hub mirror, and pushes them to
	// the LAN CI registry, where the pool legs pull them (ruling 879). A pool
	// leg never takes the artifact: it would cross the owner's WAN (ruling
	// 862). Each leg verifies its image against its own producer's ID.
	poolProducer = "pool-images"

	// hostedTerm keeps a step on GitHub-hosted runners: the artifact path of a
	// job that also runs on the pool.
	hostedTerm = "runner.environment == 'github-hosted'"

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
	// lan is the producer output carrying the image's tag in the LAN CI
	// registry (#1593), empty when the image was not pushed there.
	lan string
	// lanOut is the output of the pool producer's push step (id registry)
	// that carries that tag.
	lanOut string
	// handoff is the job env variable a consumer runs the image by, set to
	// the verified image ID by its load (hosted) or its pull (pool).
	handoff string
	stage   *regexp.Regexp
}

var imageFamilies = map[string]imageFamily{
	"toolchain": {
		artifact: "toolchain-image", image: "toolchain_image", id: "toolchain_id", ref: "toolchain_ref",
		lan: "registry_toolchain_tag", lanOut: "toolchain_tag", handoff: "TOOLCHAIN_IMAGE",
		stage: regexp.MustCompile(`scripts/in-toolchain-image\.sh`),
	},
	"it": {
		artifact: "it-image", image: "it_image", id: "it_id", ref: "it_ref",
		lan: "registry_it_tag", lanOut: "it_tag", handoff: "IT_IMAGE",
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
	ID   string            `yaml:"id"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
	Env  map[string]string `yaml:"env"`
	// ContinueOnError is a string: the key takes a boolean or an expression.
	ContinueOnError string `yaml:"continue-on-error"`
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
	if v := strings.TrimSpace(wf.Env["CI_PUBLISH"]); v != ciPublishExpr {
		problems = append(problems, "ci.yml's CI_PUBLISH is "+strconv.Quote(v)+"; want "+strconv.Quote(ciPublishExpr)+
			": only a push to main logs in to the registry or pushes to it")
	}
	if holdsRegistryToken(wf.Env) {
		problems = append(problems, "ci.yml's workflow env holds the registry token: every step of every event would have it")
	}
	for _, k := range slices.Sorted(maps.Keys(wf.Env)) {
		if v := wf.Env[k]; lanRegistryVar.MatchString(v) || strings.Contains(v, "secrets.CI_IMAGE_REGISTRY") {
			problems = append(problems, "ci.yml's workflow env "+k+" holds the LAN CI registry: want it in the env "+
				"of the jobs that reach it alone, from "+lanRegistrySecret)
		}
	}
	producer, ok := wf.Jobs[digestProducer]
	if !ok {
		return append(problems, "ci.yml has no "+digestProducer+" job: nothing builds the CI images"), nil
	}
	problems = append(problems, producerProblems(producer)...)
	if pool, ok := wf.Jobs[poolProducer]; ok {
		problems = append(problems, poolProducerProblems(pool)...)
	} else {
		problems = append(problems, "ci.yml has no "+poolProducer+" job: no pool leg could take an image without the WAN (ruling 879)")
	}

	for _, name := range slices.Sorted(maps.Keys(wf.Jobs)) {
		job := wf.Jobs[name]
		problems = append(problems, registryProblems(name, job)...)
		if name == digestProducer || name == poolProducer {
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
	problems = append(problems, digestRecordProblems(digestProducer, job)...)
	if slices.ContainsFunc(job.Steps, func(s digestStep) bool { return s.ID == lanPushID }) {
		problems = append(problems, digestProducer+" has a step with id "+lanPushID+": it runs on a GitHub-hosted runner, "+
			"which cannot reach the LAN CI registry; "+poolProducer+" pushes there (ruling 879)")
	}
	for _, family := range slices.Sorted(maps.Keys(imageFamilies)) {
		if v, ok := job.Outputs[imageFamilies[family].lan]; ok {
			problems = append(problems, digestProducer+" outputs "+imageFamilies[family].lan+" ("+strconv.Quote(v)+
				"): a LAN tag is "+poolProducer+"'s, the producer the pool legs verify against")
		}
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

// poolProducerProblems holds pool-images to ruling 879: its runs-on sends it
// to the Docker pool for exactly the events that route legs there
// (dockerRunner); for a fork's pull request it lands on a hosted runner, so
// every step that builds or reaches the registry requires lanSelfHosted and
// the job passes there with empty outputs. On the pool it builds the images,
// outputs each one's name and verified ID, pushes both to the LAN CI registry
// and fails when it cannot. It hands nothing on through GitHub: no artifact,
// no GHCR push, no package write.
func poolProducerProblems(job digestJob) []string {
	var problems []string
	if strings.TrimSpace(job.RunsOn) != dockerRunner {
		problems = append(problems, poolProducer+" runs on "+strconv.Quote(job.RunsOn)+"; want dockerRunner, the Docker pool")
	}
	for _, id := range []string{"require_registry", "build", lanPushID} {
		for _, s := range job.Steps {
			if s.ID == id && !condHasTerm(s.If, lanSelfHosted) {
				problems = append(problems, poolProducer+"'s step "+strconv.Quote(s.Name)+" (id "+id+") runs under if: "+
					strconv.Quote(s.If)+"; want "+lanSelfHosted+" among its && terms: on a fork's pull request the job "+
					"lands on a hosted runner, where it builds nothing and reaches no registry")
			}
		}
	}
	if job.Permissions["packages"] == "write" {
		problems = append(problems, poolProducer+" sets permissions packages: write: only "+digestProducer+" pushes a package")
	}
	for _, s := range job.Steps {
		if strings.HasPrefix(s.Uses, uploadArtifactAction) {
			problems = append(problems, poolProducer+"'s step "+strconv.Quote(s.Name)+" uploads an artifact: "+
				"its images go to the LAN CI registry alone; an upload would cross the owner's WAN (ruling 862)")
		}
		if imagePush.MatchString(s.Run) {
			problems = append(problems, poolProducer+"'s step "+strconv.Quote(s.Name)+" pushes to GHCR: only "+digestProducer+" publishes")
		}
	}
	if !slices.ContainsFunc(job.Steps, func(s digestStep) bool {
		return s.ID == "require_registry" && condHasTerm(s.If, "env."+lanRegistryEnv+" == ''") && strings.Contains(s.Run, "exit 1")
	}) {
		problems = append(problems, poolProducer+" has no step with id require_registry that fails when "+lanRegistryEnv+
			" is unset: the pool legs would find no image and the run would not say why")
	}
	if !slices.ContainsFunc(job.Steps, func(s digestStep) bool { return s.ID == lanPushID }) {
		problems = append(problems, poolProducer+" has no step with id "+lanPushID+": nothing pushes to the LAN CI registry")
	}
	for _, family := range slices.Sorted(maps.Keys(imageFamilies)) {
		f := imageFamilies[family]
		if want := "${{ steps." + lanPushID + ".outputs." + f.lanOut + " }}"; job.Outputs[f.lan] != want {
			problems = append(problems, poolProducer+"'s output "+f.lan+" is "+strconv.Quote(job.Outputs[f.lan])+
				"; want "+want+": the pool legs would never pull the "+family+" image")
		}
		for _, out := range []string{f.image, f.id} {
			if !strings.Contains(job.Outputs[out], "steps.") {
				problems = append(problems, poolProducer+" has no "+out+" output from a step: a pool leg would verify against nothing")
			}
		}
	}
	return problems
}

// digestRecordProblems holds a producer to the #1609 digest records:
// scripts/image-registry.sh pulls from the LAN registry only by the repo
// digest recorded in CI_IMAGE_DIGEST_DIR, so the build step names that
// directory, each image's record, keyed by its content-hash name, is restored
// from the GitHub cache before the build, and saved after it only on a push to
// main and only when the build's own push wrote a new digest.
func digestRecordProblems(digestProducer string, job digestJob) []string {
	var problems []string
	build := slices.IndexFunc(job.Steps, func(s digestStep) bool { return s.ID == "build" })
	names := slices.IndexFunc(job.Steps, func(s digestStep) bool { return s.ID == "names" })
	if build < 0 {
		return []string{digestProducer + " has no step with id build"}
	}
	if !strings.Contains(job.Steps[build].Env["CI_IMAGE_DIGEST_DIR"], "/ci-image-digests") {
		problems = append(problems, digestProducer+"'s build step sets no CI_IMAGE_DIGEST_DIR under <temp>/ci-image-digests: "+
			"no registry pull could find its record")
	}
	if names < 0 || names > build {
		problems = append(problems, digestProducer+" has no step with id names before its build step: no record has a name")
	}
	for _, f := range []string{"toolchain", "it"} {
		record := "${{ steps.names.outputs." + f + "_record }}"
		path := "${{ runner.temp }}/ci-image-digests/" + record
		key := "ci-image-digest-" + record + "-${{ github.run_id }}-${{ github.run_attempt }}"
		if names >= 0 && !strings.Contains(job.Steps[names].Run, f+"_record=") {
			problems = append(problems, digestProducer+"'s names step outputs no "+f+"_record")
		}
		if !strings.Contains(job.Steps[build].Run, f+"_recorded=true") {
			problems = append(problems, digestProducer+"'s build step outputs no "+f+"_recorded: its record is never saved")
		}
		restored := slices.ContainsFunc(job.Steps[:build], func(s digestStep) bool {
			return strings.HasPrefix(s.Uses, "actions/cache/restore@") && s.With["path"] == path &&
				s.With["key"] == key && s.With["restore-keys"] == "ci-image-digest-"+record+"-"
		})
		if !restored {
			problems = append(problems, digestProducer+" restores no "+f+" record before its build: want actions/cache/restore of "+
				path+", key "+key+", restore-keys ci-image-digest-"+record+"-")
		}
		saved := slices.ContainsFunc(job.Steps[build+1:], func(s digestStep) bool {
			return strings.HasPrefix(s.Uses, "actions/cache/save@") && s.With["path"] == path && s.With["key"] == key
		})
		if !saved {
			problems = append(problems, digestProducer+" saves no "+f+" record after its build: want actions/cache/save of "+path+", key "+key)
		}
		for _, s := range job.Steps {
			if strings.HasPrefix(s.Uses, "actions/cache/save@") && s.With["path"] == path &&
				(!strings.Contains(s.If, ciPublishTerm) || !strings.Contains(s.If, "steps.build.outputs."+f+"_recorded == 'true'")) {
				problems = append(problems, digestProducer+" saves the "+f+" record under if: "+strconv.Quote(s.If)+
					"; want "+ciPublishTerm+" and steps.build.outputs."+f+"_recorded == 'true': only main's own push writes a record")
			}
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
	problems = append(problems, lanLeakProblems(name, job)...)
	for _, s := range job.Steps {
		if lan, ok := lanRegistryProblems(name, job, s); ok {
			problems = append(problems, lan...)
			continue
		}
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

// The LAN CI registry hand-off (#1593, rulings 682/687). On a self-hosted
// runner with the repository secret CI_IMAGE_REGISTRY set, the producer pushes
// each image there, credential-free, and a consumer pulls it, checks it
// against the producer's image ID and falls back to the artifact when the
// pull or the check fails. Every event therefore still runs the image this
// commit built, which is what the GHCR rules above guard. The address is a
// secret, which the runner masks in every log line; a repository variable is
// not masked, and a run: script that interpolates it with ${{ }} is printed
// expanded, so the address reaches a step only through the job's env
// (CI_REGISTRY, which the steps' if: conditions read) and a script only as
// "$CI_REGISTRY". The address is never written down.
const (
	lanRegistryEnv    = "CI_REGISTRY"
	lanRegistrySecret = "${{ secrets.CI_IMAGE_REGISTRY }}"
	lanRegistryCond   = "env.CI_REGISTRY != ''"
	lanSelfHosted     = "runner.environment != 'github-hosted'"
	lanPushID         = "registry"
	lanPushTag        = "${{ github.run_id }}-${{ github.run_attempt }}"
	// lanTimeout bounds every LAN docker push and pull (#1609): a registry
	// that accepts the connection and stalls is cut off after
	// CI_IMAGE_REGISTRY_TIMEOUT seconds, the default scripts/image-registry.sh
	// uses, and the expiry is a miss like any other failure.
	// Every LAN step sources scripts/image-registry.sh: a transfer that
	// times out leaves the job's outage marker (ci_registry_outage) and every
	// later one in the job skips at once (ci_registry_skip), so a stalled
	// registry costs one bound per job, not one per image.
	lanTimeout = `timeout -k 10 "${CI_IMAGE_REGISTRY_TIMEOUT:-300}" `
	// lanPushRun pushes each family by its verified image ID and records its
	// tag output on success; a refused or stalled push fails the job, since
	// the pool legs have no other image (ruling 879).
	lanPushRun = `. scripts/image-registry.sh
lan_push() {
  ref="$CI_REGISTRY/go-qrdb/$2:$TAG"
  if ! ci_registry_skip "$CI_REGISTRY" "${ref#"$CI_REGISTRY"/}" &&
     docker tag "$1" "$ref" >/dev/null 2>&1 && ` + lanTimeout + `docker push -q "$ref" >/dev/null 2>&1; then
    echo "$3=$TAG" >> "$GITHUB_OUTPUT"
    docker rmi "$ref" >/dev/null 2>&1 || true
  else
    ci_registry_outage "$CI_REGISTRY" "$?"
    docker rmi "$ref" >/dev/null 2>&1 || true
    echo "::error::the push of $2 to the LAN CI registry failed: the pool legs take no other image (ruling 879)"
    exit 1
  fi
}
lan_push "$TOOLCHAIN_ID" toolchain-image toolchain_tag
lan_push "$IT_ID" it-image it_tag`
	lanPullRun = `if ! ci_registry_skip "$CI_REGISTRY" "${REF#"$CI_REGISTRY"/}" &&
   ` + lanTimeout + `docker pull -q "$REF" >/dev/null 2>&1 &&
   [ "$(docker image inspect -f '{{.Id}}' "$REF" 2>/dev/null)" = "$ID" ] &&
   docker tag "$ID" "$IMAGE" >/dev/null 2>&1; then
  docker rmi "$REF" >/dev/null 2>&1 || true
  echo "$HANDOFF=$ID" >> "$GITHUB_ENV"
else
  ci_registry_outage "$CI_REGISTRY" "$?"
  docker rmi "$REF" >/dev/null 2>&1 || true
  echo "::error::the pull of $IMAGE from the LAN CI registry failed or is not ` + poolProducer + `'s image: a pool leg takes no other (ruling 879)"
  exit 1
fi`
)

// lanLogin is a step reaching for a credential: the LAN CI registry takes none.
var lanLogin = regexp.MustCompile(`docker login|scripts/ci-image\.sh push|docker/login-action`)

// lanRegistryVar is the registry read from a repository variable, which the
// runner does not mask.
var lanRegistryVar = regexp.MustCompile(`\bvars\.CI_IMAGE_REGISTRY\b`)

// lanInterpolated is the registry interpolated with ${{ }}: in a run: script
// the Run log prints it expanded.
var lanInterpolated = regexp.MustCompile(`\$\{\{[^}]*\b(secrets\.CI_IMAGE_REGISTRY|vars\.CI_IMAGE_REGISTRY|env\.CI_REGISTRY)\b`)

// lanPullRunFor is the canonical pull step script of family f: HANDOFF is
// the family's handoff variable, so the run names no image by hand.
func lanPullRunFor(f imageFamily) string {
	return ". scripts/image-registry.sh\nHANDOFF=" + f.handoff + "\n" + `REF="$CI_REGISTRY/go-qrdb/` + f.artifact + `:$TAG"` + "\n" + lanPullRun
}

// producerOut is the expression a consumer reads docker-build's output o by.
func producerOut(o string) string {
	return "${{ needs." + digestProducer + ".outputs." + o + " }}"
}

// poolOut is the expression a pool pull reads pool-images' output o by.
func poolOut(o string) string {
	return "${{ needs." + poolProducer + ".outputs." + o + " }}"
}

// poolOutTerm is the condition that pool-images' output o is set.
func poolOutTerm(o string) string {
	return "needs." + poolProducer + ".outputs." + o + " != ''"
}

// lanPullFamily returns the family whose pool-images registry tag the step
// binds to TAG, or "".
func lanPullFamily(s digestStep) string {
	for family, f := range imageFamilies {
		if s.Env["TAG"] == poolOut(f.lan) {
			return family
		}
	}
	return ""
}

// lanLeakProblems names every place job lets the registry address reach a
// log: a value read from the unmasked variable, a run: script interpolating
// it, or a job output carrying it.
func lanLeakProblems(name string, job digestJob) []string {
	var problems []string
	viaVar := func(where, v string) {
		if lanRegistryVar.MatchString(v) {
			problems = append(problems, where+" reads vars.CI_IMAGE_REGISTRY: a repository variable is not masked; "+
				"want "+lanRegistrySecret+" in the job's env")
		}
	}
	for _, k := range slices.Sorted(maps.Keys(job.Env)) {
		viaVar(name+"'s job env "+k, job.Env[k])
	}
	for _, k := range slices.Sorted(maps.Keys(job.Outputs)) {
		if v := job.Outputs[k]; strings.Contains(v, "CI_IMAGE_REGISTRY") || strings.Contains(v, lanRegistryEnv) {
			problems = append(problems, name+"'s output "+k+" names the registry: a masked value crosses no job")
		}
	}
	for _, s := range job.Steps {
		where := name + "'s step " + strconv.Quote(s.Name)
		if m := lanInterpolated.FindString(s.Run); m != "" {
			problems = append(problems, where+" interpolates the registry with "+strconv.Quote(m)+
				" in its run: script, which the Run log prints expanded; want \"$"+lanRegistryEnv+"\"")
		}
		viaVar(where+"'s if:", s.If)
		viaVar(where+"'s run:", s.Run)
		for _, k := range slices.Sorted(maps.Keys(s.Env)) {
			viaVar(where+"'s env "+k, s.Env[k])
		}
		for _, k := range slices.Sorted(maps.Keys(s.With)) {
			viaVar(where+"'s with "+k, s.With[k])
		}
	}
	return problems
}

// lanRegistryProblems checks a step that reaches the LAN CI registry: the
// producer's push (id registry) or a consumer's pull (a run naming $CI_REGISTRY).
// ok is false for any other step, which the GHCR rules check instead.
func lanRegistryProblems(name string, job digestJob, s digestStep) (problems []string, ok bool) {
	isPush := name == poolProducer && s.ID == lanPushID
	if !isPush && !strings.Contains(s.Run, "$"+lanRegistryEnv) {
		return nil, false
	}
	where := name + "'s step " + strconv.Quote(s.Name)
	if holdsRegistryToken(s.Env) || lanLogin.MatchString(s.Run) || lanLogin.MatchString(s.Uses) {
		problems = append(problems, where+" takes a credential to the LAN CI registry, which needs none")
	}
	if v := job.Env[lanRegistryEnv]; v != lanRegistrySecret {
		problems = append(problems, name+" reaches the LAN CI registry with job env "+lanRegistryEnv+" "+strconv.Quote(v)+
			"; want "+lanRegistrySecret+": a secret is masked in every log line, and the steps' if: reads the job's env")
	}
	if !condHasTerm(s.If, lanRegistryCond) {
		problems = append(problems, where+" reaches the LAN CI registry under if: "+strconv.Quote(s.If)+
			"; want "+lanRegistryCond+" among its && terms: an unset secret keeps the artifact path alone")
	}
	if isPush {
		if !condHasTerm(s.If, lanSelfHosted) {
			problems = append(problems, where+" pushes to the LAN CI registry without "+lanSelfHosted+
				": a GitHub-hosted runner cannot reach it")
		}
		problems = append(problems, lanScriptProblems(where, s.Run, false)...)
		if strings.TrimSpace(s.Run) != lanPushRun {
			problems = append(problems, where+" runs "+strconv.Quote(firstLine(s.Run))+"; want the canonical push, "+
				"which pushes each family by its verified ID, records its tag on success and fails the job on a refused push (ruling 879)")
		}
		if s.Env["TAG"] != lanPushTag {
			problems = append(problems, where+" pushes with TAG "+strconv.Quote(s.Env["TAG"])+"; want "+lanPushTag)
		}
		return problems, true
	}
	if name == digestProducer {
		return append(problems, where+" reaches the LAN CI registry: "+digestProducer+" runs on a GitHub-hosted runner; "+
			poolProducer+" pushes there (ruling 879)"), true
	}
	if name == poolProducer {
		return append(problems, where+" reaches the LAN CI registry outside the push step (id "+lanPushID+")"), true
	}
	family := lanPullFamily(s)
	if family == "" {
		return append(problems, where+" pulls with TAG "+strconv.Quote(s.Env["TAG"])+
			"; want "+poolProducer+"'s registry_*_tag output"), true
	}
	f := imageFamilies[family]
	if s.ID != "pull_"+family {
		problems = append(problems, where+" has id "+strconv.Quote(s.ID)+"; want pull_"+family+
			", the id its download and load read")
	}
	if !condHasTerm(s.If, lanSelfHosted) {
		problems = append(problems, where+" pulls under if: "+strconv.Quote(s.If)+"; want "+lanSelfHosted+
			" among its && terms: a GitHub-hosted runner cannot reach the LAN CI registry and takes the artifact")
	}
	if condHasTerm(s.If, poolOutTerm(f.lan)) {
		problems = append(problems, where+" pulls only when "+poolOutTerm(f.lan)+": with no tag a pool leg would run "+
			"with no image; the pull runs and fails instead (ruling 879)")
	}
	problems = append(problems, lanScriptProblems(where, s.Run, true)...)
	if strings.TrimSpace(s.Run) != lanPullRunFor(f) {
		problems = append(problems, where+" runs "+strconv.Quote(firstLine(s.Run))+
			"; want the canonical pull, which checks the image against "+poolProducer+"'s ID, hands it on by that ID "+
			"and fails the job otherwise (ruling 879)")
	}
	if s.Env["IMAGE"] != poolOut(f.image) || s.Env["ID"] != poolOut(f.id) {
		problems = append(problems, where+" pulls "+f.artifact+" with IMAGE "+strconv.Quote(s.Env["IMAGE"])+
			" and ID "+strconv.Quote(s.Env["ID"])+"; want "+poolOut(f.image)+" and "+poolOut(f.id)+
			": a pool leg verifies against its own producer, "+poolProducer)
	}
	return problems, true
}

// lanDockerCmd is one registry docker command of a script, up to its && ,
// || or ; (#1595).
var lanDockerCmd = regexp.MustCompile(`docker (pull|push|tag|rmi)\s[^&|;]*`)

// lanTransfer is a registry docker pull or push with whatever precedes it on
// its line (#1609): each must be bounded by lanTimeout.
var lanTransfer = regexp.MustCompile(`(?m)^.*?docker (pull|push)\s`)

// lanScriptProblems names every way a registry script lets a rejected image
// move a tag or prints the registry's error output (#1595). A runner's docker
// daemon may be shared, so a pull checks the pulled "$REF" against "$ID"
// before any docker tag and then tags the verified "$ID", never "$REF": a
// rejected pull leaves "$IMAGE" where it was. docker's stderr names the
// registry address, which the runner masks only as a whole secret, so every
// registry docker pull, push, tag and rmi discards it.
func lanScriptProblems(where, run string, pull bool) (problems []string) {
	if pull {
		inspect := strings.Index(run, `docker image inspect -f '{{.Id}}' "$REF"`)
		tag := strings.Index(run, "docker tag ")
		switch {
		case inspect < 0:
			problems = append(problems, where+` never checks the pulled "$REF" against "$ID"`)
		case tag >= 0 && tag < inspect:
			problems = append(problems, where+` tags before it checks the pulled "$REF" against "$ID": a rejected pull moves the tag`)
		}
		if !strings.Contains(run, `docker tag "$ID" "$IMAGE"`) || strings.Contains(run, `docker tag "$REF"`) {
			problems = append(problems, where+` tags something other than the verified "$ID": want docker tag "$ID" "$IMAGE"`)
		}
	}
	for _, cmd := range lanDockerCmd.FindAllString(run, -1) {
		if !strings.Contains(cmd, "2>") {
			problems = append(problems, where+" runs "+strconv.Quote(strings.TrimSpace(cmd))+
				" without discarding its stderr, which names the registry address")
		}
	}
	for _, m := range lanTransfer.FindAllStringSubmatch(run, -1) {
		if !strings.HasSuffix(m[0], lanTimeout+"docker "+m[1]+" ") {
			problems = append(problems, where+" runs docker "+m[1]+" without "+strconv.Quote(strings.TrimSpace(lanTimeout))+
				": a stalled registry hangs the job instead of falling back to the artifact")
		}
	}
	return problems
}

// producerOutTerm is the condition that the producer's output o is set.
func producerOutTerm(o string) string {
	return "needs." + digestProducer + ".outputs." + o + " != ''"
}

// lanDownloadProblems checks the artifact download s of a family job pulls
// with the step pull: it must run exactly when the pull did not succeed, so
// its if: requires the pull's output not true and narrows by nothing the
// pull's own if: does not also require.
// lanDownloadProblems checks the artifact download s of a job that also
// pulls family f on the pool: the artifact is the hosted runners' path alone
// (ruling 879), so its if: requires hostedTerm, and nothing that a hosted run
// can lack (the registry, the pull) narrows it.
func lanDownloadProblems(name string, s, pull digestStep, f imageFamily, retry string) []string {
	where := name + "'s download of " + strconv.Quote(f.artifact)
	terms, ok := condTerms(s.If)
	if !ok || !slices.Contains(terms, hostedTerm) {
		return []string{where + " runs under if: " + strconv.Quote(s.If) + "; want " + hostedTerm +
			" among its && terms: a pool leg never takes the artifact over the owner's WAN (rulings 862, 879)"}
	}
	var problems []string
	for _, t := range terms {
		if t == lanRegistryCond || strings.Contains(t, "steps."+pull.ID+".") || strings.Contains(t, "needs."+poolProducer+".") {
			problems = append(problems, where+" also requires "+strconv.Quote(t)+
				": a hosted leg reaches neither the LAN registry nor the pull, and would load no image")
		}
	}
	return problems
}

// retryTerm is the && term that runs the second attempt of an image
// download: exactly when its first attempt, step id, failed (#1593: one
// self-hosted leg lost its artifact download while five others took it).
func retryTerm(id string) string { return "steps." + id + ".outcome == 'failure'" }

// isDownloadRetry reports whether s is the second attempt of the artifact
// download prev: the same artifact, run on prev's failure.
func isDownloadRetry(prev, s digestStep) bool {
	if prev.ID == "" || !strings.HasPrefix(prev.Uses, downloadArtifactAction) ||
		!strings.HasPrefix(s.Uses, downloadArtifactAction) || s.With["name"] != prev.With["name"] {
		return false
	}
	terms, _ := condTerms(s.If)
	return slices.Contains(terms, retryTerm(prev.ID))
}

// downloadRetryProblems checks that the image download steps[i] is a first
// attempt that tolerates its failure (an id, continue-on-error: true) and is
// followed by exactly one retry: the same action and inputs, under the first
// attempt's condition plus its failure, failing the job if it fails too.
func downloadRetryProblems(name string, steps []digestStep, i int) []string {
	s := steps[i]
	where := name + "'s download of " + strconv.Quote(s.With["name"])
	const why = ": one failed artifact download would fail the job (#1593)"
	if s.ID == "" || strings.TrimSpace(s.ContinueOnError) != "true" {
		return []string{where + " has no id or no continue-on-error: true" + why}
	}
	if i+1 >= len(steps) || !isDownloadRetry(s, steps[i+1]) {
		return []string{where + " is not followed by a retry of it under " + strconv.Quote(retryTerm(s.ID)) + why}
	}
	r := steps[i+1]
	var problems []string
	if r.Uses != s.Uses || !maps.Equal(r.With, s.With) {
		problems = append(problems, where+"'s retry runs "+strconv.Quote(r.Uses)+" with other inputs; "+
			"want the first attempt's pinned action, name and path")
	}
	if strings.TrimSpace(r.ContinueOnError) != "" {
		problems = append(problems, where+"'s retry sets continue-on-error: a second failure must fail the job")
	}
	first, _ := condTerms(s.If)
	got, _ := condTerms(r.If)
	want := append(slices.Clone(first), retryTerm(s.ID))
	slices.Sort(want)
	got = slices.Clone(got)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		problems = append(problems, where+"'s retry runs under if: "+strconv.Quote(r.If)+"; want the && terms "+
			strconv.Quote(strings.Join(want, " && "))+": the first attempt's condition and its failure")
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
		case f.lan:
			return name, "lan", true
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
	pulls := map[string]digestStep{}
	for _, s := range job.Steps {
		if family := lanPullFamily(s); family != "" && strings.Contains(s.Run, "$"+lanRegistryEnv) {
			pulls[family] = s
		}
	}
	if len(pulls) > 0 && !slices.Contains(job.Needs, poolProducer) {
		problems = append(problems, name+" pulls from the LAN CI registry but does not need "+poolProducer+
			": its tags and IDs would be empty")
	}
	downloaded, loaded := map[string]int{}, map[string]int{}
	for i, s := range job.Steps {
		family := lanPullFamily(s)
		isPull := family != "" && strings.TrimSpace(s.Run) == lanPullRunFor(imageFamilies[family])
		if imageRebuild.MatchString(s.Run) && !isPull {
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
			retry := ""
			if i > 0 && isDownloadRetry(job.Steps[i-1], s) {
				// The second attempt: checked with its first attempt.
				retry = retryTerm(job.Steps[i-1].ID)
			} else {
				problems = append(problems, downloadRetryProblems(name, job.Steps, i)...)
			}
			for fam, f := range imageFamilies {
				if pull, ok := pulls[fam]; ok && f.artifact == art {
					problems = append(problems, lanDownloadProblems(name, s, pull, f, retry)...)
				}
			}
		}
		for _, m := range imageLoad.FindAllStringSubmatch(s.Run, -1) {
			problems = append(problems, loadProblems(name, job, s, i, m, downloaded, loaded, pulls)...)
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
	downloaded, loaded map[string]int, pulls map[string]digestStep) []string {
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
	if _, ok := pulls[family]; ok {
		if !condHasTerm(s.If, hostedTerm) {
			problems = append(problems, where+" loads the "+family+" artifact under if: "+strconv.Quote(s.If)+"; want "+hostedTerm+
				" among its && terms: a pool leg has no artifact; its pull hands the image on (ruling 879)")
		}
		if _, bound := s.Env["PULLED"]; bound || strings.Contains(s.Run, "$PULLED") {
			problems = append(problems, where+" reads PULLED: the pull and the load run on different runners, never both")
		}
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
	terms, ok := condTerms(cond)
	return ok && slices.Contains(terms, term)
}

// condTerms splits the step condition cond into its top-level && operands.
// ok is false when anything in it (|| or a negated group) could turn it true
// without one of them.
func condTerms(cond string) (terms []string, ok bool) {
	c := strings.TrimSpace(cond)
	c = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(c, "${{"), "}}"))
	if strings.Contains(c, "||") || strings.Contains(c, "!(") {
		return nil, false
	}
	if c == "" {
		return nil, true
	}
	for _, part := range strings.Split(c, "&&") {
		terms = append(terms, strings.TrimSpace(part))
	}
	return terms, true
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
      - name: Name the images
        id: names
        run: |
          t=$(make -s toolchain-image)
          echo "toolchain_record=${t//:/_}" >> "$GITHUB_OUTPUT"
          i=$(make -s it-image)
          echo "it_record=${i//:/_}" >> "$GITHUB_OUTPUT"
      - name: Restore the toolchain record
        uses: actions/cache/restore@0000000000000000000000000000000000000000 # v0
        with:
          path: ${{ runner.temp }}/ci-image-digests/${{ steps.names.outputs.toolchain_record }}
          key: ci-image-digest-${{ steps.names.outputs.toolchain_record }}-${{ github.run_id }}-${{ github.run_attempt }}
          restore-keys: ci-image-digest-${{ steps.names.outputs.toolchain_record }}-
      - name: Restore the integration record
        uses: actions/cache/restore@0000000000000000000000000000000000000000 # v0
        with:
          path: ${{ runner.temp }}/ci-image-digests/${{ steps.names.outputs.it_record }}
          key: ci-image-digest-${{ steps.names.outputs.it_record }}-${{ github.run_id }}-${{ github.run_attempt }}
          restore-keys: ci-image-digest-${{ steps.names.outputs.it_record }}-
      - name: Build the images
        id: build
        env:
          CI_IMAGE_DIGEST_DIR: ${{ format('{0}/ci-image-digests', runner.temp) }}
        run: |
          make docker-build
          echo "toolchain_recorded=true" >> "$GITHUB_OUTPUT"
          echo "it_recorded=true" >> "$GITHUB_OUTPUT"
      - name: Save the toolchain record
        if: ${{ env.CI_PUBLISH == 'true' && steps.build.outputs.toolchain_recorded == 'true' }}
        uses: actions/cache/save@0000000000000000000000000000000000000000 # v0
        with:
          path: ${{ runner.temp }}/ci-image-digests/${{ steps.names.outputs.toolchain_record }}
          key: ci-image-digest-${{ steps.names.outputs.toolchain_record }}-${{ github.run_id }}-${{ github.run_attempt }}
      - name: Save the integration record
        if: ${{ env.CI_PUBLISH == 'true' && steps.build.outputs.it_recorded == 'true' }}
        uses: actions/cache/save@0000000000000000000000000000000000000000 # v0
        with:
          path: ${{ runner.temp }}/ci-image-digests/${{ steps.names.outputs.it_record }}
          key: ci-image-digest-${{ steps.names.outputs.it_record }}-${{ github.run_id }}-${{ github.run_attempt }}
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
  pool-images:
    if: ${{ !github.event.pull_request.draft }}
    runs-on: ${{ ((github.event_name == 'push' && github.ref == 'refs/heads/main') || (github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository)) && fromJSON('["self-hosted","linux","x64","bigdisk-docker"]') || 'ubuntu-latest' }}
    outputs:
      toolchain_image: ${{ steps.build.outputs.toolchain_image }}
      toolchain_id: ${{ steps.build.outputs.toolchain_id }}
      it_image: ${{ steps.build.outputs.it_image }}
      it_id: ${{ steps.build.outputs.it_id }}
      registry_toolchain_tag: ${{ steps.registry.outputs.toolchain_tag }}
      registry_it_tag: ${{ steps.registry.outputs.it_tag }}
    env:
      CI_REGISTRY: ${{ secrets.CI_IMAGE_REGISTRY }}
    steps:
      - name: Require the LAN CI registry
        id: require_registry
        if: ${{ env.CI_REGISTRY == '' && runner.environment != 'github-hosted' }}
        run: |
          echo "::error::no LAN CI registry: the pool legs would take no image"
          exit 1
      - name: Build the images
        id: build
        if: ${{ runner.environment != 'github-hosted' }}
        run: |
          make docker-build
          echo "toolchain_id=$(make -s toolchain-image-id)" >> "$GITHUB_OUTPUT"
      - name: Push to the LAN CI registry
        id: registry
        if: ${{ env.CI_REGISTRY != '' && runner.environment != 'github-hosted' }}
        env:
          TAG: ${{ github.run_id }}-${{ github.run_attempt }}
          TOOLCHAIN_ID: ${{ steps.build.outputs.toolchain_id }}
          IT_ID: ${{ steps.build.outputs.it_id }}
        run: |
          . scripts/image-registry.sh
          lan_push() {
            ref="$CI_REGISTRY/go-qrdb/$2:$TAG"
            if ! ci_registry_skip "$CI_REGISTRY" "${ref#"$CI_REGISTRY"/}" &&
               docker tag "$1" "$ref" >/dev/null 2>&1 && timeout -k 10 "${CI_IMAGE_REGISTRY_TIMEOUT:-300}" docker push -q "$ref" >/dev/null 2>&1; then
              echo "$3=$TAG" >> "$GITHUB_OUTPUT"
              docker rmi "$ref" >/dev/null 2>&1 || true
            else
              ci_registry_outage "$CI_REGISTRY" "$?"
              docker rmi "$ref" >/dev/null 2>&1 || true
              echo "::error::the push of $2 to the LAN CI registry failed: the pool legs take no other image (ruling 879)"
              exit 1
            fi
          }
          lan_push "$TOOLCHAIN_ID" toolchain-image toolchain_tag
          lan_push "$IT_ID" it-image it_tag
  lint:
    runs-on: ubuntu-latest
    needs: [preflight, docker-build, pool-images]
    env:
      CI_REGISTRY: ${{ secrets.CI_IMAGE_REGISTRY }}
    steps:
      - name: Pull the toolchain image from the LAN CI registry
        id: pull_toolchain
        if: ${{ env.CI_REGISTRY != '' && runner.environment != 'github-hosted' }}
        env:
          TAG: ${{ needs.pool-images.outputs.registry_toolchain_tag }}
          IMAGE: ${{ needs.pool-images.outputs.toolchain_image }}
          ID: ${{ needs.pool-images.outputs.toolchain_id }}
        run: |
          . scripts/image-registry.sh
          HANDOFF=TOOLCHAIN_IMAGE
          REF="$CI_REGISTRY/go-qrdb/toolchain-image:$TAG"
          if ! ci_registry_skip "$CI_REGISTRY" "${REF#"$CI_REGISTRY"/}" &&
             timeout -k 10 "${CI_IMAGE_REGISTRY_TIMEOUT:-300}" docker pull -q "$REF" >/dev/null 2>&1 &&
             [ "$(docker image inspect -f '{{.Id}}' "$REF" 2>/dev/null)" = "$ID" ] &&
             docker tag "$ID" "$IMAGE" >/dev/null 2>&1; then
            docker rmi "$REF" >/dev/null 2>&1 || true
            echo "$HANDOFF=$ID" >> "$GITHUB_ENV"
          else
            ci_registry_outage "$CI_REGISTRY" "$?"
            docker rmi "$REF" >/dev/null 2>&1 || true
            echo "::error::the pull of $IMAGE from the LAN CI registry failed or is not pool-images's image: a pool leg takes no other (ruling 879)"
            exit 1
          fi
      - name: Download the toolchain image
        id: download_toolchain
        if: ${{ runner.environment == 'github-hosted' }}
        continue-on-error: true
        uses: actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0
        with:
          name: toolchain-image
          path: ${{ runner.temp }}/toolchain-image
      - name: Download the toolchain image (retry)
        if: ${{ runner.environment == 'github-hosted' && steps.download_toolchain.outcome == 'failure' }}
        uses: actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0
        with:
          name: toolchain-image
          path: ${{ runner.temp }}/toolchain-image
      - name: Load the toolchain image
        if: ${{ runner.environment == 'github-hosted' }}
        env:
          IMAGE: ${{ needs.docker-build.outputs.toolchain_image }}
          ID: ${{ needs.docker-build.outputs.toolchain_id }}
        run: |
          scripts/ci-image.sh load "$RUNNER_TEMP/toolchain-image" "$IMAGE" "$ID"
          echo "TOOLCHAIN_IMAGE=$ID" >> "$GITHUB_ENV"
      - name: make lint
        run: scripts/in-toolchain-image.sh "$TOOLCHAIN_IMAGE" make lint
  legs:
    runs-on: ubuntu-latest
    needs: [docker-build, pool-images]
    env:
      CI_REGISTRY: ${{ secrets.CI_IMAGE_REGISTRY }}
      FULL: 'true'
    steps:
      - name: Pull the integration image from the LAN CI registry
        id: pull_it
        if: ${{ env.FULL == 'true' && env.CI_REGISTRY != '' && runner.environment != 'github-hosted' }}
        env:
          TAG: ${{ needs.pool-images.outputs.registry_it_tag }}
          IMAGE: ${{ needs.pool-images.outputs.it_image }}
          ID: ${{ needs.pool-images.outputs.it_id }}
        run: |
          . scripts/image-registry.sh
          HANDOFF=IT_IMAGE
          REF="$CI_REGISTRY/go-qrdb/it-image:$TAG"
          if ! ci_registry_skip "$CI_REGISTRY" "${REF#"$CI_REGISTRY"/}" &&
             timeout -k 10 "${CI_IMAGE_REGISTRY_TIMEOUT:-300}" docker pull -q "$REF" >/dev/null 2>&1 &&
             [ "$(docker image inspect -f '{{.Id}}' "$REF" 2>/dev/null)" = "$ID" ] &&
             docker tag "$ID" "$IMAGE" >/dev/null 2>&1; then
            docker rmi "$REF" >/dev/null 2>&1 || true
            echo "$HANDOFF=$ID" >> "$GITHUB_ENV"
          else
            ci_registry_outage "$CI_REGISTRY" "$?"
            docker rmi "$REF" >/dev/null 2>&1 || true
            echo "::error::the pull of $IMAGE from the LAN CI registry failed or is not pool-images's image: a pool leg takes no other (ruling 879)"
            exit 1
          fi
      - name: Download the integration image
        id: download_it
        if: ${{ env.FULL == 'true' && runner.environment == 'github-hosted' }}
        continue-on-error: true
        uses: actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0
        with:
          name: it-image
          path: ${{ runner.temp }}/it-image
      - name: Download the integration image (retry)
        if: ${{ env.FULL == 'true' && runner.environment == 'github-hosted' && steps.download_it.outcome == 'failure' }}
        uses: actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0
        with:
          name: it-image
          path: ${{ runner.temp }}/it-image
      - name: Load the integration image
        if: ${{ runner.environment == 'github-hosted' }}
        env:
          IMAGE: ${{ needs.docker-build.outputs.it_image }}
          ID: ${{ needs.docker-build.outputs.it_id }}
        run: |
          scripts/ci-image.sh load "$RUNNER_TEMP/it-image" "$IMAGE" "$ID"
          echo "IT_IMAGE=$ID" >> "$GITHUB_ENV"
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
        id: download_it
        continue-on-error: true
        with:
          name: it-image
          path: ${{ runner.temp }}/it-image
      - name: Retry the download
        if: ${{ steps.download_it.outcome == 'failure' }}
        uses: actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0
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
		lintLoad = `scripts/ci-image.sh load "$RUNNER_TEMP/toolchain-image" "$IMAGE" "$ID"`
		legsTest = "        run: make integration-test IT_FEATURE=audit IT_PREBUILT=1\n"
	)
	for _, tc := range []struct{ name, old, new string }{
		// Ruling 879: two producers, the pool path failing closed.
		{name: "pool-images not needed", old: "needs: [preflight, docker-build, pool-images]", new: "needs: [preflight, docker-build]"},
		{name: "docker-build not needed", old: "needs: [preflight, docker-build, pool-images]", new: "needs: [preflight, pool-images]"},
		{name: "a LAN pull without the registry check", old: "if: ${{ env.CI_REGISTRY != '' && runner.environment != 'github-hosted' }}\n        env:\n          TAG: ${{ needs.pool-images.outputs.registry_toolchain_tag", new: "if: ${{ runner.environment != 'github-hosted' }}\n        env:\n          TAG: ${{ needs.pool-images.outputs.registry_toolchain_tag"},
		{name: "a LAN pull on a GitHub-hosted runner", old: "if: ${{ env.CI_REGISTRY != '' && runner.environment != 'github-hosted' }}\n        env:\n          TAG: ${{ needs.pool-images.outputs.registry_toolchain_tag", new: "if: ${{ env.CI_REGISTRY != '' }}\n        env:\n          TAG: ${{ needs.pool-images.outputs.registry_toolchain_tag"},
		{name: "a LAN pull that skips an empty tag", old: "if: ${{ env.CI_REGISTRY != '' && runner.environment != 'github-hosted' }}\n        env:\n          TAG: ${{ needs.pool-images.outputs.registry_toolchain_tag", new: "if: ${{ needs.pool-images.outputs.registry_toolchain_tag != '' && env.CI_REGISTRY != '' && runner.environment != 'github-hosted' }}\n        env:\n          TAG: ${{ needs.pool-images.outputs.registry_toolchain_tag"},
		{name: "a LAN pull of the other family's tag", old: "TAG: ${{ needs.pool-images.outputs.registry_toolchain_tag }}", new: "TAG: ${{ needs.pool-images.outputs.registry_it_tag }}"},
		{name: "a LAN pull checked against docker-build's ID", old: "ID: ${{ needs.pool-images.outputs.toolchain_id }}", new: "ID: ${{ needs.docker-build.outputs.toolchain_id }}"},
		{name: "a LAN pull checked against the other family's ID", old: "ID: ${{ needs.pool-images.outputs.toolchain_id }}", new: "ID: ${{ needs.pool-images.outputs.it_id }}"},
		{name: "a LAN pull that falls back to the artifact", old: "            exit 1\n          fi\n      - name: Download the toolchain image", new: "          fi\n      - name: Download the toolchain image"},
		{name: "a LAN push that warns and goes on", old: "              exit 1\n            fi\n          }", new: "            fi\n          }"},
		{name: "a LAN push that records a tag it did not push", old: "            else\n              ci_registry_outage \"$CI_REGISTRY\" \"$?\"\n              docker rmi \"$ref\"", new: "            else\n              echo \"$3=$TAG\" >> \"$GITHUB_OUTPUT\"\n              ci_registry_outage \"$CI_REGISTRY\" \"$?\"\n              docker rmi \"$ref\""},
		{name: "a LAN push by tag rather than ID", old: "lan_push \"$TOOLCHAIN_ID\" toolchain-image toolchain_tag", new: "lan_push \"$TOOLCHAIN_IMAGE\" toolchain-image toolchain_tag"},
		{name: "a pool producer that does not require the registry", old: "        id: require_registry\n", new: "        id: registry_note\n"},
		{name: "a pool producer that uploads", old: "      - name: Push to the LAN CI registry\n        id: registry\n", new: "      - name: Upload\n        uses: actions/upload-artifact@b7c566a772e6b6bfb58ed0dc250532a479d7789f # v6.0.0\n        with:\n          name: toolchain-image\n          path: x\n      - name: Push to the LAN CI registry\n        id: registry\n"},
		{name: "a pool producer on a hosted runner", old: "    runs-on: ${{ ((github.event_name", new: "    runs-on: ubuntu-latest\n    labels: ${{ ((github.event_name"},
		{name: "a pool build that runs on a hosted runner", old: "        id: build\n        if: ${{ runner.environment != 'github-hosted' }}\n", new: "        id: build\n"},
		{name: "a registry requirement that fails a hosted run", old: "if: ${{ env.CI_REGISTRY == '' && runner.environment != 'github-hosted' }}", new: "if: ${{ env.CI_REGISTRY == '' }}"},
		{name: "no pool producer", old: "  pool-images:\n", new: "  pool-images-old:\n"},
		{name: "a pool tag output from another step", old: "registry_toolchain_tag: ${{ steps.registry.outputs.toolchain_tag }}", new: "registry_toolchain_tag: ${{ steps.build.outputs.toolchain_tag }}"},
		{name: "a pool producer without the registry env", old: "    env:\n      CI_REGISTRY: ${{ secrets.CI_IMAGE_REGISTRY }}\n    steps:\n      - name: Require the LAN CI registry\n", new: "    steps:\n      - name: Require the LAN CI registry\n"},
		{name: "docker-build outputs a LAN tag", old: "      it_ref: ${{ steps.push.outputs.it_ref }}\n", new: "      it_ref: ${{ steps.push.outputs.it_ref }}\n      registry_it_tag: ${{ steps.push.outputs.it_ref }}\n"},
		{name: "a hosted download that runs on the pool", old: "        id: download_toolchain\n        if: ${{ runner.environment == 'github-hosted' }}\n", new: "        id: download_toolchain\n        if: ${{ always() }}\n"},
		{name: "a hosted download gated on the registry", old: "        id: download_toolchain\n        if: ${{ runner.environment == 'github-hosted' }}\n", new: "        id: download_toolchain\n        if: ${{ env.CI_REGISTRY != '' && runner.environment == 'github-hosted' }}\n"},
		{name: "a hosted download gated on the pull", old: "        id: download_toolchain\n        if: ${{ runner.environment == 'github-hosted' }}\n", new: "        id: download_toolchain\n        if: ${{ runner.environment == 'github-hosted' && steps.pull_toolchain.outcome == 'skipped' }}\n"},
		{name: "an integration download that runs on the pool", old: "if: ${{ env.FULL == 'true' && runner.environment == 'github-hosted' }}\n        continue-on-error", new: "if: ${{ env.FULL == 'true' }}\n        continue-on-error"},
		{name: "a hosted load that runs on the pool", old: "      - name: Load the toolchain image\n        if: ${{ runner.environment == 'github-hosted' }}\n", new: "      - name: Load the toolchain image\n"},
		{name: "a load that reads PULLED", old: "          scripts/ci-image.sh load \"$RUNNER_TEMP/toolchain-image\" \"$IMAGE\" \"$ID\"", new: "          [ \"$PULLED\" = true ] || scripts/ci-image.sh load \"$RUNNER_TEMP/toolchain-image\" \"$IMAGE\" \"$ID\""},
		{name: "the registry from a variable", old: "    needs: [preflight, docker-build, pool-images]\n    env:\n      CI_REGISTRY: ${{ secrets.CI_IMAGE_REGISTRY }}\n", new: "    needs: [preflight, docker-build, pool-images]\n    env:\n      CI_REGISTRY: ${{ vars.CI_IMAGE_REGISTRY }}\n"},
		{name: "a pull job without the registry env", old: "    needs: [preflight, docker-build, pool-images]\n    env:\n      CI_REGISTRY: ${{ secrets.CI_IMAGE_REGISTRY }}\n", new: "    needs: [preflight, docker-build, pool-images]\n"},
		{name: "a first attempt that fails the job", old: "        id: download_toolchain\n        if: ${{ runner.environment == 'github-hosted' }}\n        continue-on-error: true\n", new: "        id: download_toolchain\n        if: ${{ runner.environment == 'github-hosted' }}\n"},
		{name: "a retry that drops the hosted term", old: "if: ${{ runner.environment == 'github-hosted' && steps.download_toolchain.outcome == 'failure' }}", new: "if: ${{ steps.download_toolchain.outcome == 'failure' }}"},
		{name: "a retry that drops the first attempt's term", old: "if: ${{ env.FULL == 'true' && runner.environment == 'github-hosted' && steps.download_it.outcome == 'failure' }}", new: "if: ${{ runner.environment == 'github-hosted' && steps.download_it.outcome == 'failure' }}"},
		{name: "no digest record directory", old: "          CI_IMAGE_DIGEST_DIR: ${{ format('{0}/ci-image-digests', runner.temp) }}\n",
			new: "          DOCS_ONLY: x\n"},
		{name: "a record saved by a pull request", old: "if: ${{ env.CI_PUBLISH == 'true' && steps.build.outputs.it_recorded == 'true' }}",
			new: "if: ${{ steps.build.outputs.it_recorded == 'true' }}"},
		{name: "a record saved when no push wrote one", old: "if: ${{ env.CI_PUBLISH == 'true' && steps.build.outputs.toolchain_recorded == 'true' }}",
			new: "if: ${{ env.CI_PUBLISH == 'true' }}"},
		{name: "no restore of a record", old: "      - name: Restore the integration record\n        uses: actions/cache/restore@",
			new: "      - name: Restore the integration record\n        uses: actions/upload-artifact@"},
		{name: "a record keyed by something else", old: "restore-keys: ci-image-digest-${{ steps.names.outputs.toolchain_record }}-",
			new: "restore-keys: ci-image-digest-"},
		{name: "a record path off its name", old: "          path: ${{ runner.temp }}/ci-image-digests/${{ steps.names.outputs.it_record }}\n          key: ci-image-digest-${{ steps.names.outputs.it_record }}-${{ github.run_id }}-${{ github.run_attempt }}\n          restore",
			new: "          path: ${{ runner.temp }}/ci-image-digests\n          key: ci-image-digest-${{ steps.names.outputs.it_record }}-${{ github.run_id }}-${{ github.run_attempt }}\n          restore"},
		{name: "no record names", old: `echo "it_record=${i//:/_}" >> "$GITHUB_OUTPUT"`,
			new: `echo "it_name=$i" >> "$GITHUB_OUTPUT"`},
		{name: "no recorded output", old: `echo "toolchain_recorded=true" >> "$GITHUB_OUTPUT"`,
			new: `echo "toolchain_saved=true" >> "$GITHUB_OUTPUT"`},
		{name: "the probe-and-rebuild form", old: lintLoad,
			new: `docker image inspect "$IMAGE" || make docker-build-toolchain`},
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
			new: `scripts/ci-image.sh load "$RUNNER_TEMP/it-image" "$IMAGE" "$ID"`},
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
		{name: "a LAN pull without the ID check", old: `[ "$(docker image inspect -f '{{.Id}}' "$REF" 2>/dev/null)" = "$ID" ]`, new: `true`},
		{name: "a LAN push printing the registry's stderr", old: `docker push -q "$ref" >/dev/null 2>&1;`, new: `docker push -q "$ref" >/dev/null;`},
		{name: "a LAN push tag printing the registry's stderr", old: `docker tag "$1" "$ref" >/dev/null 2>&1 &&`, new: `docker tag "$1" "$ref" &&`},
		// #1609: a stalled registry is cut off, never waited on.
		{name: "a LAN push with no timeout", old: lanTimeout + `docker push -q "$ref"`, new: `docker push -q "$ref"`},
		{name: "a LAN pull with no timeout", old: lanTimeout + `docker pull -q "$REF"`, new: `docker pull -q "$REF"`},
		{name: "a LAN pull with a bound of its own", old: lanTimeout + `docker pull -q "$REF"`, new: `timeout 99999 docker pull -q "$REF"`},
		{name: "a LAN pull under another id", old: "        id: pull_toolchain\n", new: "        id: pull\n"},
		{name: "a LAN push on a GitHub-hosted runner", old: "env.CI_REGISTRY != '' && runner.environment != 'github-hosted'",
			new: "env.CI_REGISTRY != ''"},
		{name: "a LAN push with the token", old: "          TAG: ${{ github.run_id }}-${{ github.run_attempt }}\n",
			new: "          TAG: ${{ github.run_id }}-${{ github.run_attempt }}\n          CI_REGISTRY_TOKEN: ${{ secrets.GITHUB_TOKEN }}\n"},
		{name: "the workflow env holds the token", old: "env:\n  CI_PUBLISH:", new: "env:\n  GH: ${{ secrets.GITHUB_TOKEN }}\n  CI_PUBLISH:"},
		// #1595 Codex round 1: the producer's tag outputs and push step.
		{name: "no toolchain tag output", old: "      registry_toolchain_tag: ${{ steps.registry.outputs.toolchain_tag }}\n", new: ""},
		{name: "no integration tag output", old: "      registry_it_tag: ${{ steps.registry.outputs.it_tag }}\n", new: ""},
		{name: "a tag output from another step", old: "registry_it_tag: ${{ steps.registry.outputs.it_tag }}",
			new: "registry_it_tag: ${{ steps.push.outputs.it_tag }}"},
		{name: "a LAN push without its id", old: "        id: registry\n", new: ""},
		{name: "a pull run: interpolating the registry secret", old: `REF="$CI_REGISTRY/go-qrdb/toolchain-image:$TAG"`,
			new: `REF="${{ secrets.CI_IMAGE_REGISTRY }}/go-qrdb/toolchain-image:$TAG"`},
		{name: "a push run: interpolating the registry env", old: `ref="$CI_REGISTRY/go-qrdb/$2:$TAG"`,
			new: `ref="${{ env.CI_REGISTRY }}/go-qrdb/$2:$TAG"`},
		{name: "a run: masking the registry variable", old: "      - name: make lint\n",
			new: "      - name: Mask\n        run: echo \"::add-mask::${{ vars.CI_IMAGE_REGISTRY }}\"\n      - name: make lint\n"},
		{name: "the registry in the workflow env", old: "env:\n  CI_PUBLISH:", new: "env:\n  CI_REGISTRY: ${{ secrets.CI_IMAGE_REGISTRY }}\n  CI_PUBLISH:"},
		{name: "an output naming the registry", old: "      registry_it_tag: ${{ steps.registry.outputs.it_tag }}\n",
			new: "      registry_it_tag: ${{ steps.registry.outputs.it_tag }}\n      registry: ${{ env.CI_REGISTRY }}\n"},
		// #1593: every image download is retried once.
		{name: "a download without its retry",
			old: "      - name: Retry the download\n        if: ${{ steps.download_it.outcome == 'failure' }}\n",
			new: "      - name: Something else\n        if: ${{ always() }}\n"},
		{name: "a first attempt without an id", old: "        id: download_it\n        continue-on-error: true\n",
			new: "        continue-on-error: true\n"},
		{name: "a retry that tolerates its failure",
			old: "        if: ${{ steps.download_it.outcome == 'failure' }}\n",
			new: "        if: ${{ steps.download_it.outcome == 'failure' }}\n        continue-on-error: true\n"},
		{name: "a retry with other inputs",
			old: "        if: ${{ steps.download_it.outcome == 'failure' }}\n        uses: actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0\n        with:\n          name: it-image\n          path: ${{ runner.temp }}/it-image\n",
			new: "        if: ${{ steps.download_it.outcome == 'failure' }}\n        uses: actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0\n        with:\n          name: it-image\n          path: ${{ runner.temp }}/it-image\n          github-token: x\n"},
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

// TestCIImageDigest_LANVerifyBeforeTag pins #1595's two Codex findings for
// both image families: the pull checks the pulled "$REF" against "$ID" before
// any docker tag and tags the verified "$ID", and no registry docker command
// prints its stderr. The canonical scripts pass, each regressed form fails
// lanScriptProblems on its own and the whole gate on the fixture.
func TestCIImageDigest_LANVerifyBeforeTag(t *testing.T) {
	if p := lanScriptProblems("push", lanPushRun, false); len(p) != 0 {
		t.Fatalf("the canonical push fails its own gate: %s", strings.Join(p, "; "))
	}
	const (
		skip    = `if ! ci_registry_skip "$CI_REGISTRY" "${REF#"$CI_REGISTRY"/}" &&`
		pull    = `timeout -k 10 "${CI_IMAGE_REGISTRY_TIMEOUT:-300}" docker pull -q "$REF" >/dev/null 2>&1 &&`
		inspect = `[ "$(docker image inspect -f '{{.Id}}' "$REF" 2>/dev/null)" = "$ID" ] &&`
		tagID   = `docker tag "$ID" "$IMAGE" >/dev/null 2>&1; then`
	)
	regressions := []struct{ name, old, new string }{
		{name: "the pre-#1595 tag-then-check pull", old: pull + "\n             " + inspect + "\n             " + tagID,
			new: `docker pull -q "$REF" >/dev/null && docker tag "$REF" "$IMAGE" &&` + "\n             " +
				`[ "$(docker image inspect -f '{{.Id}}' "$IMAGE")" = "$ID" ]; then`},
		{name: "a tag of the registry ref", old: tagID, new: `docker tag "$REF" "$IMAGE" >/dev/null 2>&1; then`},
		{name: "a tag before the check", old: pull, new: pull + ` docker tag "$ID" "$IMAGE" >/dev/null 2>&1 &&`},
		{name: "a check of the tagged image", old: inspect, new: `[ "$(docker image inspect -f '{{.Id}}' "$IMAGE" 2>/dev/null)" = "$ID" ] &&`},
		{name: "a pull printing its stderr", old: pull, new: lanTimeout + `docker pull -q "$REF" >/dev/null &&`},
		{name: "a pull with no timeout", old: pull, new: `docker pull -q "$REF" >/dev/null 2>&1 &&`},
		{name: "a pull bounded by a timeout of its own", old: pull, new: `timeout 99999 docker pull -q "$REF" >/dev/null 2>&1 &&`},
		{name: "a tag printing its stderr", old: tagID, new: `docker tag "$ID" "$IMAGE"; then`},
		{name: "an rmi printing its stderr", old: `docker rmi "$REF" >/dev/null 2>&1`, new: `docker rmi "$REF" >/dev/null`},
	}
	for _, family := range []string{"toolchain", "it"} {
		f := imageFamilies[family]
		canon := lanPullRunFor(f)
		if p := lanScriptProblems(family, canon, true); len(p) != 0 {
			t.Fatalf("the canonical %s pull fails its own gate: %s", family, strings.Join(p, "; "))
		}
		head := `REF="$CI_REGISTRY/go-qrdb/` + f.artifact + `:$TAG"` + "\n          "
		for _, tc := range regressions {
			t.Run(family+"/"+tc.name, func(t *testing.T) {
				if !strings.Contains(digestFixture, head+skip+"\n             "+pull) {
					t.Fatalf("the fixture has no %s pull: the case tests nothing", family)
				}
				i := strings.Index(digestFixture, head)
				j := strings.Index(digestFixture[i:], tc.old)
				if j < 0 {
					t.Fatalf("the fixture's %s pull has no %q: the case tests nothing", family, tc.old)
				}
				mutated := digestFixture[:i+j] + tc.new + digestFixture[i+j+len(tc.old):]
				if problems, _ := digestProblems([]byte(mutated), 3); len(problems) == 0 {
					t.Fatalf("a %s pull with %s passed the gate", family, tc.name)
				}
				script := strings.Replace(strings.ReplaceAll(canon, "\n   ", "\n             "), tc.old, tc.new, 1)
				if script == strings.ReplaceAll(canon, "\n   ", "\n             ") {
					t.Fatalf("the canonical %s pull has no %q: the case tests nothing", family, tc.old)
				}
				if p := lanScriptProblems(family, script, true); len(p) == 0 {
					t.Fatalf("a %s pull with %s passed lanScriptProblems", family, tc.name)
				}
			})
		}
	}
}
