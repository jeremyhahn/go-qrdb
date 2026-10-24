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

// ci.yml builds the CI images once per run, in docker-build, which pushes
// them to the Gitea registry and outputs their digest references,
// toolchain_ref and it_ref; every other job that runs a stage in an image
// pulls exactly that digest with scripts/ci-registry.sh (DS-3516). Each
// property below is one edit from going false without anything else
// noticing:
//
//   - a consumer that checks the daemon for an image and rebuilds it when it
//     is missing (`docker image inspect || make docker-build-toolchain`, the
//     form this replaced): it passes on the image it built, which is not the
//     one docker-build pushed and the other jobs ran;
//   - a consumer that reads an output other than a digest reference, or runs
//     an image it never pulled;
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
	// digestProducer is the one job that builds and pushes the CI images.
	digestProducer = "docker-build"

	// digestRefPattern is the shape docker-build validates every output
	// against before a consumer starts.
	digestRefPattern = `^[^@ ]+@sha256:[0-9a-f]{64}$`

	// digestConsumerFloor is a discovery floor: lint, image, fips,
	// integration-features and soak consume the images today.
	digestConsumerFloor = 5
)

// digestOutputs are the outputs a consumer may read: the digest references.
var digestOutputs = []string{"toolchain_ref", "it_ref"}

var (
	producerOutputRef = regexp.MustCompile(`needs\.docker-build\.outputs\.([A-Za-z0-9_-]+)`)
	registryPull      = regexp.MustCompile(`scripts/ci-registry\.sh pull "\$([A-Za-z_][A-Za-z0-9_]*)"`)
	imageStage        = regexp.MustCompile(`scripts/in-toolchain-image\.sh|make integration-test\s`)
	imageRebuild      = regexp.MustCompile(`docker image inspect|docker (buildx )?build|make (-s )?docker-build|docker-build-(toolchain|it)`)
)

type digestWorkflow struct {
	Jobs map[string]struct {
		RunsOn      string            `yaml:"runs-on"`
		Needs       []string          `yaml:"needs"`
		Permissions map[string]string `yaml:"permissions"`
		Outputs     map[string]string `yaml:"outputs"`
		Env         map[string]string `yaml:"env"`
		Steps       []struct {
			Name string            `yaml:"name"`
			Run  string            `yaml:"run"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// digestProblems returns every way body departs from the properties above,
// and the consumers it found.
func digestProblems(body []byte, floor int) (problems, consumers []string) {
	var wf digestWorkflow
	if err := yaml.Unmarshal(body, &wf); err != nil {
		return []string{"ci.yml is not a workflow this gate can read: " + err.Error()}, nil
	}
	producer, ok := wf.Jobs[digestProducer]
	if !ok {
		return []string{"ci.yml has no " + digestProducer + " job: nothing pushes the CI images"}, nil
	}
	problems = append(problems, producerProblems(producer.Permissions, producer.Outputs, stepRuns(producer.Steps))...)

	names := make([]string, 0, len(wf.Jobs))
	for name := range wf.Jobs {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if name == digestProducer {
			continue
		}
		job := wf.Jobs[name]
		read := map[string]bool{}
		for _, v := range job.Env {
			markOutputs(read, v)
		}
		stage := -1
		for i, s := range job.Steps {
			markOutputs(read, s.Run)
			for _, v := range s.Env {
				markOutputs(read, v)
			}
			if stage < 0 && imageStage.MatchString(s.Run) {
				stage = i
			}
		}
		if len(read) == 0 {
			if stage >= 0 {
				problems = append(problems, name+" runs a stage in an image ("+strconv.Quote(firstLine(job.Steps[stage].Run))+
					") but reads no "+digestProducer+" output: it runs an image docker-build did not push")
			}
			continue
		}
		consumers = append(consumers, name)
		problems = append(problems, consumerProblems(name, job.RunsOn, job.Needs, job.Permissions, job.Env, job.Steps, read, stage)...)
	}
	if len(consumers) < floor {
		problems = append(problems, "found "+strconv.Itoa(len(consumers))+" consumers of "+digestProducer+
			"'s images; want at least "+strconv.Itoa(floor)+": the parse found too little to check")
	}
	return problems, consumers
}

func producerProblems(perms, outputs map[string]string, runs []string) []string {
	var problems []string
	if perms["packages"] != "write" {
		problems = append(problems, digestProducer+" does not set permissions packages: write: its push would be refused")
	}
	for _, out := range digestOutputs {
		if !strings.Contains(outputs[out], "steps.") {
			problems = append(problems, digestProducer+" has no "+out+" output from a step: a consumer would pull nothing")
		}
	}
	all := strings.Join(runs, "\n")
	if !strings.Contains(all, "scripts/ci-registry.sh push ") {
		problems = append(problems, digestProducer+" does not push with scripts/ci-registry.sh")
	}
	if !strings.Contains(all, digestRefPattern) {
		problems = append(problems, digestProducer+" does not validate its outputs against "+strconv.Quote(digestRefPattern))
	}
	return problems
}

func consumerProblems(name, runsOn string, needs []string, perms, jobEnv map[string]string, steps []struct {
	Name string            `yaml:"name"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}, read map[string]bool, stage int) []string {
	var problems []string
	if !slices.Contains(needs, digestProducer) {
		problems = append(problems, name+" reads "+digestProducer+"'s outputs but does not need it: they would be empty")
	}
	if strings.Contains(runsOn, digestProducer) {
		problems = append(problems, name+"'s runs-on "+strconv.Quote(runsOn)+" follows "+digestProducer+
			": a consumer pulls on any runner and has no host affinity")
	}
	if p := perms["packages"]; p != "read" && p != "write" {
		problems = append(problems, name+" does not set permissions packages: read: its pull would be refused")
	}
	for out := range read {
		if !slices.Contains(digestOutputs, out) {
			problems = append(problems, name+" reads "+digestProducer+".outputs."+out+
				"; want only the digest references "+strings.Join(digestOutputs, ", "))
		}
	}
	pulled := map[string]bool{}
	first := -1
	for i, s := range steps {
		if imageRebuild.MatchString(s.Run) {
			problems = append(problems, name+"'s step "+strconv.Quote(s.Name)+" builds or probes for an image ("+
				strconv.Quote(firstLine(s.Run))+"): a consumer pulls docker-build's digest and never rebuilds")
		}
		for _, m := range registryPull.FindAllStringSubmatch(s.Run, -1) {
			v, ok := s.Env[m[1]]
			if !ok {
				v = jobEnv[m[1]]
			}
			out := producerOutputRef.FindStringSubmatch(v)
			if out == nil || strings.TrimSpace(v) != "${{ needs.docker-build.outputs."+out[1]+" }}" {
				problems = append(problems, name+" pulls $"+m[1]+", which is "+strconv.Quote(v)+
					"; want it bound to a "+digestProducer+" digest output")
				continue
			}
			pulled[out[1]] = true
			if first < 0 {
				first = i
			}
		}
	}
	for _, out := range digestOutputs {
		if read[out] && !pulled[out] {
			problems = append(problems, name+" reads "+out+" but never pulls it with scripts/ci-registry.sh pull")
		}
	}
	if stage >= 0 && (first < 0 || first > stage) {
		problems = append(problems, name+" runs a stage before it pulls the image the stage runs in")
	}
	return problems
}

func markOutputs(read map[string]bool, s string) {
	for _, m := range producerOutputRef.FindAllStringSubmatch(s, -1) {
		read[m[1]] = true
	}
}

func stepRuns(steps []struct {
	Name string            `yaml:"name"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}) []string {
	runs := make([]string, 0, len(steps))
	for _, s := range steps {
		runs = append(runs, s.Run)
	}
	return runs
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
	t.Logf("consumers of %s's images: %s", digestProducer, strings.Join(consumers, ", "))
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

const digestFixture = `jobs:
  docker-build:
    runs-on: bigdisk
    permissions:
      contents: read
      packages: write
    outputs:
      toolchain_ref: ${{ steps.push.outputs.toolchain_ref }}
      it_ref: ${{ steps.push.outputs.it_ref }}
    steps:
      - id: push
        run: scripts/ci-registry.sh push "$TOOLCHAIN_IMAGE" qrdb-ci-toolchain "$TRUST"
      - run: re='^[^@ ]+@sha256:[0-9a-f]{64}$'
  lint:
    runs-on: bigdisk
    needs: [changes, docker-build]
    permissions:
      contents: read
      packages: read
    steps:
      - name: Pull the toolchain image
        env:
          TOOLCHAIN_REF: ${{ needs.docker-build.outputs.toolchain_ref }}
        run: scripts/ci-registry.sh pull "$TOOLCHAIN_REF" "$TOOLCHAIN_HOLD"
      - name: make lint
        run: scripts/in-toolchain-image.sh "$TOOLCHAIN_IMAGE" make lint
  legs:
    runs-on: bigdisk
    needs: [docker-build]
    permissions:
      packages: read
    env:
      IT_IMAGE: ${{ needs.docker-build.outputs.it_ref }}
    steps:
      - name: Pull the integration image
        run: scripts/ci-registry.sh pull "$IT_IMAGE" "$IT_HOLD"
      - name: Integration tests
        run: make integration-test IT_FEATURE=audit
  typescript:
    runs-on: ubuntu-latest
    steps:
      - run: make test-typescript
`

func TestCIImageDigest_FixtureIsValid(t *testing.T) {
	problems, consumers := digestProblems([]byte(digestFixture), 2)
	if len(problems) != 0 {
		t.Fatalf("the valid fixture must have no problems, got %v", problems)
	}
	if !slices.Equal(consumers, []string{"legs", "lint"}) {
		t.Fatalf("consumers = %v; want [legs lint]", consumers)
	}
}

func TestCIImageDigest_CanFail(t *testing.T) {
	for _, tc := range []struct{ name, old, new string }{
		{name: "the probe-and-rebuild form", old: `run: scripts/ci-registry.sh pull "$TOOLCHAIN_REF" "$TOOLCHAIN_HOLD"`,
			new: `run: docker image inspect "$TOOLCHAIN_REF" || make docker-build-toolchain`},
		{name: "a rebuild beside the pull", old: "        run: make integration-test IT_FEATURE=audit\n",
			new: "        run: make docker-build-it && make integration-test IT_FEATURE=audit\n"},
		{name: "a buildx build", old: "        run: make integration-test IT_FEATURE=audit\n",
			new: "        run: docker buildx build . && make integration-test IT_FEATURE=audit\n"},
		{name: "a tag output", old: "outputs.it_ref }}\n    steps:\n      - name: Pull the integration image", new: "outputs.it_image }}\n    steps:\n      - name: Pull the integration image"},
		{name: "an output read but not pulled", old: "        run: scripts/ci-registry.sh pull \"$IT_IMAGE\" \"$IT_HOLD\"\n", new: "        run: echo \"$IT_IMAGE\"\n"},
		{name: "a pull of an unbound variable", old: `pull "$IT_IMAGE" "$IT_HOLD"`, new: `pull "$OTHER" "$IT_HOLD"`},
		{name: "a stage with no image output", old: "      - run: make test-typescript\n", new: "      - run: scripts/in-toolchain-image.sh qrdb-toolchain:latest make test\n"},
		{name: "a stage before the pull", old: "      - name: Pull the toolchain image\n        env:\n          TOOLCHAIN_REF: ${{ needs.docker-build.outputs.toolchain_ref }}\n        run: scripts/ci-registry.sh pull \"$TOOLCHAIN_REF\" \"$TOOLCHAIN_HOLD\"\n      - name: make lint\n        run: scripts/in-toolchain-image.sh \"$TOOLCHAIN_IMAGE\" make lint\n",
			new: "      - name: make lint\n        run: scripts/in-toolchain-image.sh \"$TOOLCHAIN_IMAGE\" make lint\n      - name: Pull the toolchain image\n        env:\n          TOOLCHAIN_REF: ${{ needs.docker-build.outputs.toolchain_ref }}\n        run: scripts/ci-registry.sh pull \"$TOOLCHAIN_REF\" \"$TOOLCHAIN_HOLD\"\n"},
		{name: "docker-build not needed", old: "needs: [changes, docker-build]", new: "needs: [changes]"},
		{name: "host affinity", old: "  lint:\n    runs-on: bigdisk", new: "  lint:\n    runs-on: ${{ needs.docker-build.outputs.runner }}"},
		{name: "no packages read", old: "    permissions:\n      packages: read\n", new: ""},
		{name: "no packages write", old: "packages: write", new: "packages: read"},
		{name: "no push", old: `scripts/ci-registry.sh push "$TOOLCHAIN_IMAGE"`, new: `docker push "$TOOLCHAIN_IMAGE"`},
		{name: "no validation", old: `re='^[^@ ]+@sha256:[0-9a-f]{64}$'`, new: `echo done`},
		{name: "an output not from a step", old: "it_ref: ${{ steps.push.outputs.it_ref }}", new: "it_ref: qrdb-ci-it:latest"},
		{name: "no producer", old: "  docker-build:\n", new: "  docker-build-old:\n"},
		{name: "too few consumers", old: "      IT_IMAGE: ${{ needs.docker-build.outputs.it_ref }}\n", new: "      IT_IMAGE: qrdb-ci-it:latest\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(digestFixture, tc.old, tc.new, 1)
			if body == digestFixture {
				t.Fatal("the mutation did not apply: the case tests the valid fixture")
			}
			if problems, _ := digestProblems([]byte(body), 2); len(problems) == 0 {
				t.Fatal("expected at least one problem, got none")
			}
		})
	}
}
