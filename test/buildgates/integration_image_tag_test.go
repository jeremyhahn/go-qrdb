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

// Every integration run builds and runs its OWN image. The compose stack was
// already namespaced per run (INT_PROJECT), but every run built into and ran
// the one fixed tag `go-qrdb-dev:latest`, so four CI runs that landed on one
// host at once rebuilt it under each other: the test container's CLI called
// `FTSIndex` and the server, baked from another train's tree, answered
// "unknown method FTSIndex". Nothing failed until a test did, and the failure
// named a missing RPC rather than a shared tag.
//
// This gate holds the four pieces that make the image per-run, because each is
// one edit from silently undoing it:
//
//   - every compose service that BUILDS an image names it through a required
//     interpolation, `${VAR:?...}` -- a literal tag, or a `${VAR:-default}`,
//     is a shared tag again;
//   - the Makefile exports each such VAR from a variable derived from
//     $(INT_PROJECT), and `integration-image` tags its build with it;
//   - every Makefile stage that brings a stack `up` names the project and ends
//     with `integration-down`, which removes the stack AND the run's images,
//     so per-run tags do not accumulate on a host;
//   - no workflow runs `docker compose` itself (the required interpolation
//     refuses a bare `down`, and the `|| true` a teardown carries would hide
//     that), and every job that runs a compose stage tears down through
//     `make integration-down`, under `always()`, with its own INT_PROJECT.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	intProjectRef  = "$(INT_PROJECT)"
	intImageEnv    = "QRDB_INT_IMAGE"
	intDownTarget  = "integration-down"
	intImageTarget = "integration-image"
	intComposeDir  = "test/integration/api"
	intBuiltFloor  = 5 // qrdbd, qrdbd-shamir, integration-tests + the GPU overlay's two
	intStageFloor  = 5 // standalone, cluster, gpu, embedding, rag
	intCIJobFloor  = 1 // standalone; the self-spawned suites run per feature, outside compose
)

var (
	// requiredInterpolation is the only accepted `image:` for a built
	// service: `${VAR:?message}`. `${VAR}` and `${VAR:-x}` both fall back to
	// something every run shares (an empty name, or x).
	requiredInterpolation = regexp.MustCompile(`^\$\{([A-Z][A-Z0-9_]*):\?[^}]*\}$`)

	// makeTargetLine matches a rule line (`name: prereqs`), not an assignment.
	makeTargetLine = regexp.MustCompile(`^([A-Za-z0-9_.%-]+):([^=]|$)`)

	composeUp       = regexp.MustCompile(`(^|\s)up\s`)
	shellCommandSep = regexp.MustCompile(`;|&&|\|\||\n`)
	intProjectFlag  = regexp.MustCompile(`INT_PROJECT="([^"]+)"`)
)

// composeService is the part of a compose service this gate reads.
type composeService struct {
	Build yaml.Node `yaml:"build"`
	Image string    `yaml:"image"`
}

// composeImageProblems returns the image variables the compose file's built
// services name, how many built services it has, and every built service whose
// image is not a required per-run interpolation.
func composeImageProblems(name string, body []byte) (vars []string, built int, problems []string) {
	var doc struct {
		Services map[string]composeService `yaml:"services"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, 0, []string{name + ": not valid YAML: " + err.Error()}
	}
	seen := make(map[string]bool)
	for svc, s := range doc.Services {
		if s.Build.Kind == 0 {
			continue // pulled, not built: an upstream image no run rebuilds
		}
		built++
		m := requiredInterpolation.FindStringSubmatch(s.Image)
		if m == nil {
			problems = append(problems, name+": service "+svc+" builds into image \""+s.Image+
				"\"; a built image must be a required per-run interpolation ${VAR:?...}, because a "+
				"literal tag or a ${VAR:-default} is shared by every concurrent run on the host")
			continue
		}
		if !seen[m[1]] {
			seen[m[1]] = true
			vars = append(vars, m[1])
		}
	}
	sort.Strings(vars)
	return vars, built, problems
}

// makeRecipes maps each rule in a Makefile to its recipe text.
func makeRecipes(makefile string) map[string]string {
	recipes := make(map[string]string)
	cur := ""
	for _, line := range strings.Split(makefile, "\n") {
		switch {
		case strings.HasPrefix(line, "\t"):
			if cur != "" {
				recipes[cur] += line + "\n"
			}
		case makeTargetLine.MatchString(line):
			cur = makeTargetLine.FindStringSubmatch(line)[1]
			if _, ok := recipes[cur]; !ok {
				recipes[cur] = ""
			}
		case strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#"):
		default:
			cur = ""
		}
	}
	return recipes
}

// composeUpCommands returns each `docker compose ... up` command in a recipe,
// with its line continuations joined, so a flag is checked on the command
// that brings the stack up and not on a neighbouring `down`.
func composeUpCommands(recipe string) []string {
	var ups []string
	flat := strings.ReplaceAll(recipe, "\\\n", " ")
	for _, cmd := range shellCommandSep.Split(flat, -1) {
		if strings.Contains(cmd, "docker compose") && composeUp.MatchString(cmd) {
			ups = append(ups, cmd)
		}
	}
	return ups
}

// makeAssignment returns the right-hand side of `name := value` (or `=`,
// `?=`), and whether the Makefile assigns name at all.
func makeAssignment(makefile, name string) (string, bool) {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `\s*[:?]?=\s*(.*?)\s*$`)
	m := re.FindStringSubmatch(makefile)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// makefileImageProblems checks that each compose image variable is exported
// from a per-run Makefile variable, that the image build and the teardown use
// those variables, and returns the stages that bring a stack up, sorted.
func makefileImageProblems(makefile string, vars []string) (stages []string, problems []string) {
	recipes := makeRecipes(makefile)
	makeVars := make(map[string]string) // compose env var -> Makefile variable
	for _, v := range vars {
		re := regexp.MustCompile(`(?m)^export\s+` + regexp.QuoteMeta(v) + `\s*:?=\s*\$\(([A-Za-z_][A-Za-z0-9_]*)\)\s*$`)
		m := re.FindStringSubmatch(makefile)
		if m == nil {
			problems = append(problems, "Makefile does not `export "+v+" := $(<per-run variable>)`; "+
				"the compose files require it, so every stage would be refused")
			continue
		}
		val, ok := makeAssignment(makefile, m[1])
		if !ok || !strings.Contains(val, intProjectRef) {
			problems = append(problems, "Makefile's "+m[1]+" (exported as "+v+") is \""+val+
				"\", which is not derived from "+intProjectRef+": every run on the host shares it")
			continue
		}
		makeVars[v] = m[1]
	}

	if mv, ok := makeVars[intImageEnv]; ok {
		if !strings.Contains(recipes[intImageTarget], "-t $("+mv+")") {
			problems = append(problems, "Makefile's "+intImageTarget+" does not tag its build `-t $("+mv+
				")`: the stack would run an image this run did not build")
		}
	} else if len(problems) == 0 {
		problems = append(problems, "no compose file requires "+intImageEnv+
			": the integration stack's image is not named per run")
	}

	down, ok := recipes[intDownTarget]
	if !ok {
		problems = append(problems, "Makefile has no "+intDownTarget+" target")
	} else {
		if !strings.Contains(down, "-p "+intProjectRef) || !strings.Contains(down, "down -v") {
			problems = append(problems, intDownTarget+" does not `docker compose -p "+intProjectRef+" down -v`")
		}
		if !strings.Contains(down, "docker image rm") {
			problems = append(problems, intDownTarget+" removes no image: per-run tags would pile up on the host")
		}
		for _, mv := range makeVars {
			if !strings.Contains(down, "$("+mv+")") {
				problems = append(problems, intDownTarget+" does not remove $("+mv+")")
			}
		}
	}

	names := make([]string, 0, len(recipes))
	for name := range recipes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r := recipes[name]
		if !strings.Contains(r, intComposeDir) {
			continue
		}
		ups := composeUpCommands(r)
		if len(ups) == 0 {
			continue
		}
		stages = append(stages, name)
		for _, cmd := range ups {
			if !strings.Contains(cmd, "-p "+intProjectRef) {
				problems = append(problems, "Makefile target "+name+" brings the integration stack up without -p "+
					intProjectRef+": it shares the default project with every other run")
			}
		}
		if !strings.Contains(r, intDownTarget) {
			problems = append(problems, "Makefile target "+name+" brings the integration stack up but never runs "+
				intDownTarget+": its containers and per-run image outlive it")
		}
	}
	return stages, problems
}

// workflowImageProblems checks one workflow: no step runs `docker compose`
// itself, and every job that runs a compose stage tears it down through
// `make integration-down` under always() with the same INT_PROJECT. A compose
// stage is one of stages, the Makefile targets that bring a stack up: a
// `make integration-test-<feature>` step runs the self-spawned suites on the
// runner itself and has no stack to tear down. It returns how many such jobs
// it found.
func workflowImageProblems(name string, body []byte, stages []string) (jobs int, problems []string) {
	quoted := make([]string, 0, len(stages))
	for _, s := range stages {
		quoted = append(quoted, regexp.QuoteMeta(s))
	}
	runsStage := regexp.MustCompile(`(^|\s)make\s+(` + strings.Join(quoted, "|") + `)(\s|$)`)
	var wf struct {
		Jobs map[string]struct {
			Steps []workflowStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		return 0, []string{name + ": not valid YAML: " + err.Error()}
	}
	jobNames := make([]string, 0, len(wf.Jobs))
	for j := range wf.Jobs {
		jobNames = append(jobNames, j)
	}
	sort.Strings(jobNames)
	for _, j := range jobNames {
		var project string
		stage := false
		for _, s := range wf.Jobs[j].Steps {
			if strings.Contains(s.Run, "docker compose") {
				problems = append(problems, name+": job "+j+" runs `docker compose` directly; the compose files "+
					"require the run's image name, so go through make ("+intDownTarget+" for a teardown)")
			}
			if len(stages) > 0 && runsStage.MatchString(s.Run) {
				stage = true
				if m := intProjectFlag.FindStringSubmatch(s.Run); m != nil {
					project = m[1]
				}
			}
		}
		if !stage {
			continue
		}
		jobs++
		torn := false
		for _, s := range wf.Jobs[j].Steps {
			if !strings.Contains(s.Run, "make "+intDownTarget) {
				continue
			}
			torn = true
			if !strings.Contains(s.If, "always()") {
				problems = append(problems, name+": job "+j+"'s "+intDownTarget+" step does not run under "+
					"always(): a failed or cancelled run leaves its stack and image behind")
			}
			m := intProjectFlag.FindStringSubmatch(s.Run)
			if m == nil || m[1] != project {
				got := ""
				if m != nil {
					got = m[1]
				}
				problems = append(problems, name+": job "+j+" runs its stage as INT_PROJECT \""+project+
					"\" but tears down \""+got+"\": the teardown would remove another run's stack, or none")
			}
		}
		if !torn {
			problems = append(problems, name+": job "+j+" runs an integration compose stage with no `make "+
				intDownTarget+"` step: an interrupted run leaks its stack and image")
		}
	}
	return jobs, problems
}

func TestIntegrationImage_PerRunTags(t *testing.T) {
	root := repoRoot(t)

	composes, err := filepath.Glob(filepath.Join(root, intComposeDir, "docker-compose*.yml"))
	if err != nil {
		t.Fatalf("glob compose files: %v", err)
	}
	var vars []string
	seen := make(map[string]bool)
	built := 0
	for _, path := range composes {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		rel, _ := filepath.Rel(root, path)
		v, n, problems := composeImageProblems(rel, body)
		built += n
		for _, p := range problems {
			t.Error(p)
		}
		for _, name := range v {
			if !seen[name] {
				seen[name] = true
				vars = append(vars, name)
			}
		}
	}
	if built < intBuiltFloor {
		t.Fatalf("found %d built services in %s/docker-compose*.yml, below the floor of %d: a gate that "+
			"finds nothing to check reports the same green as one that checked everything", built, intComposeDir, intBuiltFloor)
	}

	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	stages, problems := makefileImageProblems(string(makefile), vars)
	for _, p := range problems {
		t.Error(p)
	}
	if len(stages) < intStageFloor {
		t.Fatalf("found %d Makefile stages that bring %s up, below the floor of %d", len(stages), intComposeDir, intStageFloor)
	}

	var workflows []string
	for _, dir := range []string{".github/workflows", ".gitea/workflows"} {
		for _, ext := range []string{"*.yml", "*.yaml"} {
			m, err := filepath.Glob(filepath.Join(root, dir, ext))
			if err != nil {
				t.Fatalf("glob workflows: %v", err)
			}
			workflows = append(workflows, m...)
		}
	}
	jobs := 0
	for _, path := range workflows {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		rel, _ := filepath.Rel(root, path)
		n, problems := workflowImageProblems(rel, body, stages)
		jobs += n
		for _, p := range problems {
			t.Error(p)
		}
	}
	if jobs < intCIJobFloor {
		t.Fatalf("found %d workflow jobs running an integration compose stage, below the floor of %d", jobs, intCIJobFloor)
	}
}

const (
	intImageCompose = `x-int-image: &int-image "${QRDB_INT_IMAGE:?unset}"
services:
  qrdbd:
    build:
      context: ../../..
    image: *int-image
  integration-tests:
    build:
      context: ../../..
    image: *int-image
  ollama:
    image: ollama/ollama:latest
`

	intImageMakefile = `INT_PROJECT ?= qrdb-int-host
INT_IMAGE := go-qrdb-int:$(INT_PROJECT)
export QRDB_INT_IMAGE := $(INT_IMAGE)

.PHONY: integration-image
integration-image:
	docker build --target integration -t $(INT_IMAGE) .

.PHONY: integration-test-standalone
integration-test-standalone: integration-image
	@cd test/integration/api && docker compose -p $(INT_PROJECT) down -v 2>/dev/null; true
	@cd test/integration/api && \
		docker compose -p $(INT_PROJECT) up --abort-on-container-exit integration-tests; \
		rc=$$?; \
		$(MAKE) -s -C $(CURDIR) integration-down INT_PROJECT=$(INT_PROJECT); \
		exit $$rc

.PHONY: integration-down
integration-down:
	@cd test/integration/api && docker compose -p $(INT_PROJECT) down -v --remove-orphans || true
	@docker image rm $(INT_IMAGE) >/dev/null 2>&1 || true
`

	intImageWorkflow = `jobs:
  integration:
    steps:
      - run: make integration-test-auth
      - run: |
          export INT_PROJECT="qrdb-int-${GITHUB_RUN_ID:-local}"
          make integration-test-standalone INT_PROJECT="$INT_PROJECT"
      - if: always()
        run: make integration-down INT_PROJECT="qrdb-int-${GITHUB_RUN_ID:-local}" || true
  features:
    steps:
      - run: make integration-test-${{ matrix.feature }}
  lint:
    steps:
      - run: make lint
`
)

func intImageFixtureProblems(compose, makefile, workflow string) []string {
	vars, _, problems := composeImageProblems("compose", []byte(compose))
	stages, mp := makefileImageProblems(makefile, vars)
	_, wp := workflowImageProblems("workflow", []byte(workflow), stages)
	return append(append(problems, mp...), wp...)
}

func TestIntegrationImageProblems_ValidFixture(t *testing.T) {
	vars, built, problems := composeImageProblems("compose", []byte(intImageCompose))
	if len(problems) != 0 || built != 2 || len(vars) != 1 || vars[0] != intImageEnv {
		t.Fatalf("compose fixture: vars %v, built %d, problems %v; want [%s], 2, none", vars, built, problems, intImageEnv)
	}
	stages, problems := makefileImageProblems(intImageMakefile, vars)
	if len(problems) != 0 || len(stages) != 1 || stages[0] != "integration-test-standalone" {
		t.Fatalf("Makefile fixture: stages %v, problems %v; want [integration-test-standalone], none", stages, problems)
	}
	// The features job runs a per-feature target, which is not a stage: it
	// must count neither as a compose job nor as one missing its teardown.
	jobs, problems := workflowImageProblems("workflow", []byte(intImageWorkflow), stages)
	if len(problems) != 0 || jobs != 1 {
		t.Fatalf("workflow fixture: jobs %d, problems %v; want 1, none", jobs, problems)
	}
}

func TestIntegrationImageProblems_CanFail(t *testing.T) {
	if p := intImageFixtureProblems(intImageCompose, intImageMakefile, intImageWorkflow); len(p) != 0 {
		t.Fatalf("the valid fixtures must have no problems, got %v; a broken fixture would invalidate "+
			"every case below", p)
	}

	cases := []struct {
		name                        string
		compose, makefile, workflow string
	}{
		{
			name:    "literal shared tag in compose",
			compose: strings.Replace(intImageCompose, "image: *int-image\n  ollama", "image: go-qrdb-dev:latest\n  ollama", 1),
		},
		{
			name:    "interpolation with a shared default",
			compose: strings.Replace(intImageCompose, "${QRDB_INT_IMAGE:?unset}", "${QRDB_INT_IMAGE:-go-qrdb-dev:latest}", 1),
		},
		{
			name:     "INT_IMAGE not derived from INT_PROJECT",
			makefile: strings.Replace(intImageMakefile, "go-qrdb-int:$(INT_PROJECT)", "go-qrdb-dev:latest", 1),
		},
		{
			name:     "image variable not exported",
			makefile: strings.Replace(intImageMakefile, "export QRDB_INT_IMAGE := $(INT_IMAGE)\n", "", 1),
		},
		{
			name:     "integration-image tags a fixed name",
			makefile: strings.Replace(intImageMakefile, "-t $(INT_IMAGE) .", "-t go-qrdb-dev:latest .", 1),
		},
		{
			name:     "integration-down removes no image",
			makefile: strings.Replace(intImageMakefile, "\t@docker image rm $(INT_IMAGE) >/dev/null 2>&1 || true\n", "", 1),
		},
		{
			name: "stage tears down with a bare compose down",
			makefile: strings.Replace(intImageMakefile,
				"$(MAKE) -s -C $(CURDIR) integration-down INT_PROJECT=$(INT_PROJECT)",
				"docker compose -p $(INT_PROJECT) down -v", 1),
		},
		{
			name: "stage brings the stack up without a project",
			makefile: strings.Replace(intImageMakefile,
				"docker compose -p $(INT_PROJECT) up", "docker compose up", 1),
		},
		{
			name: "workflow tears down with a bare compose down",
			workflow: strings.Replace(intImageWorkflow,
				`make integration-down INT_PROJECT="qrdb-int-${GITHUB_RUN_ID:-local}" || true`,
				`docker compose -p "qrdb-int-${GITHUB_RUN_ID:-local}" down -v || true`, 1),
		},
		{
			name:     "workflow teardown not under always()",
			workflow: strings.Replace(intImageWorkflow, "      - if: always()\n        run: make integration-down", "      - run: make integration-down", 1),
		},
		{
			name: "workflow stage job has no teardown",
			workflow: strings.Replace(intImageWorkflow,
				"      - if: always()\n        run: make integration-down INT_PROJECT=\"qrdb-int-${GITHUB_RUN_ID:-local}\" || true\n", "", 1),
		},
		{
			name:     "workflow teardown names another project",
			workflow: strings.Replace(intImageWorkflow, `integration-down INT_PROJECT="qrdb-int-`, `integration-down INT_PROJECT="qrdb-int-cluster-`, 1),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compose, makefile, workflow := tc.compose, tc.makefile, tc.workflow
			if compose == "" {
				compose = intImageCompose
			}
			if makefile == "" {
				makefile = intImageMakefile
			}
			if workflow == "" {
				workflow = intImageWorkflow
			}
			if compose == intImageCompose && makefile == intImageMakefile && workflow == intImageWorkflow {
				t.Fatal("the mutation did not apply: the case tests the valid fixture")
			}
			if p := intImageFixtureProblems(compose, makefile, workflow); len(p) == 0 {
				t.Fatal("expected at least one problem, got none")
			}
		})
	}
}

func TestMakeRecipes(t *testing.T) {
	recipes := makeRecipes(intImageMakefile)
	if !strings.Contains(recipes[intDownTarget], "docker image rm") {
		t.Fatalf("integration-down recipe = %q; want its docker image rm line", recipes[intDownTarget])
	}
	if _, ok := recipes["INT_IMAGE"]; ok {
		t.Fatal("an assignment was parsed as a rule")
	}
	if strings.Contains(recipes[intImageTarget], "docker compose") {
		t.Fatal("a recipe ran on into the next rule")
	}
}

func TestComposeUpCommands(t *testing.T) {
	recipe := "\t@cd test/integration/api && docker compose -p $(INT_PROJECT) down -v; true\n" +
		"\t@cd test/integration/api && \\\n\t\tdocker compose -f a.yml \\\n\t\t\tup --build x; \\\n\t\trc=$$?\n"
	ups := composeUpCommands(recipe)
	if len(ups) != 1 || !strings.Contains(ups[0], "-f a.yml") || !strings.Contains(ups[0], "up --build") {
		t.Fatalf("composeUpCommands = %q; want the one continued up command", ups)
	}
	if got := composeUpCommands("\tdocker compose -p p logs qrdbd\n"); len(got) != 0 {
		t.Fatalf("composeUpCommands found %q in a recipe with no up", got)
	}
}
