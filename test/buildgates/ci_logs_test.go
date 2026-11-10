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

// ci-template v1.21.1 (handbook ci-standard.md §5): a CI log carries no
// credential, and v1.21 moved the build-tag set into ci.yml's env, where
// nothing compared it with the Makefile's. Three properties, each one edit
// from going false with no other check noticing:
//
//   - the lint and image jobs and every integration job mask the forge credentials as
//     their first step after the checkout, the token inside
//     GIT_CONFIG_KEY_0's URL included;
//   - no step prints an env file or the job environment (`env`, `printenv`,
//     `set -x`, a `cat` of a .env or the module credential file);
//   - GO_BUILD_TAGS, which replaces the Makefile's composed set wherever it is
//     set, is byte for byte what `make -s print-build-tags` composes, so a
//     tag added to the Makefile cannot silently drop out of CI.

import (
	"bytes"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// credentialJobs are the jobs that run a step which could print the forge
// credentials: a make target, a script, npm or curl. The runner injects the
// credentials into every job (dependency-resolution.md §6), so each of these
// masks them in its first step after the checkout.
var credentialJobs = []string{"docker-build", "lint", "lint-gates", "image", "coverage", "coverage-merge", "test-clusterproof", "coverage-fts", "test-tags", "test-sdk", "test-typescript", "preflight", "integration-standalone", "integration-features", "fips", "soak"}

// unmaskedJobs are the jobs that hold the credentials but run only inline
// steps that cannot print them, each with the reason. Every job in ci.yml is
// in one of the two lists, so a new job fails the gate until it is placed.
var unmaskedJobs = map[string]string{
	"ci-gate": "prints only the jobs' results",
}

const maskStepName = "Mask the forge credentials"

// envPrint matches a step line that prints the job environment or an env file.
var envPrint = regexp.MustCompile(`(?m)\bprintenv\b|\bset -x\b|\bset -o xtrace\b|(^|[;&|(]\s*)env\s*($|[|;&>)])|\b(cat|head|tail|less|more)\s+\S*(\.env|gomodauth\S*|MODULE_ENV_FILE)\b`)

type ciLogStep struct {
	Name string `yaml:"name"`
	Uses string `yaml:"uses"`
	Run  string `yaml:"run"`
}

type ciLogWorkflow struct {
	Env  map[string]string `yaml:"env"`
	Jobs map[string]struct {
		Steps []ciLogStep `yaml:"steps"`
	} `yaml:"jobs"`
}

func parseLogWorkflow(t *testing.T, body string) ciLogWorkflow {
	t.Helper()
	var wf ciLogWorkflow
	if err := yaml.Unmarshal([]byte(body), &wf); err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	return wf
}

func readLogWorkflow(t *testing.T) ciLogWorkflow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	return parseLogWorkflow(t, string(b))
}

// maskProblems reports every credential job whose first step after the
// checkout is not the masking step, or whose masking step misses a variable.
func maskProblems(wf ciLogWorkflow) []string {
	var problems []string
	for _, name := range slices.Sorted(maps.Keys(wf.Jobs)) {
		if _, ok := unmaskedJobs[name]; !ok && !slices.Contains(credentialJobs, name) {
			problems = append(problems, name+": in neither credentialJobs nor unmaskedJobs")
		}
	}
	for _, name := range credentialJobs {
		job, ok := wf.Jobs[name]
		if !ok {
			problems = append(problems, name+": job missing")
			continue
		}
		i := 0
		for i < len(job.Steps) && strings.HasPrefix(job.Steps[i].Uses, "actions/checkout@") {
			i++
		}
		if i == len(job.Steps) || job.Steps[i].Name != maskStepName {
			problems = append(problems, name+": the first step after the checkout is not "+maskStepName)
			continue
		}
		run := job.Steps[i].Run
		for _, want := range []string{"::add-mask::", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "*://*:*@*"} {
			if !strings.Contains(run, want) {
				problems = append(problems, name+": "+maskStepName+" does not handle "+want)
			}
		}
	}
	return problems
}

// envPrintProblems reports every step whose run script prints the job
// environment or an env file.
func envPrintProblems(wf ciLogWorkflow) []string {
	var problems []string
	for name, job := range wf.Jobs {
		for _, step := range job.Steps {
			if m := envPrint.FindString(step.Run); m != "" {
				problems = append(problems, name+"/"+step.Name+": prints the environment ("+strings.TrimSpace(m)+")")
			}
		}
	}
	slices.Sort(problems)
	return problems
}

// goBuildTagsProblem compares ci.yml's GO_BUILD_TAGS with the Makefile's
// composed set; the order matters too, because the coverage baseline records
// the set as written.
func goBuildTagsProblem(ci, makefile string) string {
	ci, makefile = strings.TrimSpace(ci), strings.TrimSpace(makefile)
	if ci == "" || makefile == "" {
		return "a tag set is empty: ci.yml GO_BUILD_TAGS " + strconv.Quote(ci) + ", make " + strconv.Quote(makefile)
	}
	if ci != makefile {
		return "ci.yml GO_BUILD_TAGS is " + strconv.Quote(ci) + " but `make -s print-build-tags` composes " + strconv.Quote(makefile)
	}
	return ""
}

// composedBuildTags runs `make -s print-build-tags` with GO_BUILD_TAGS cleared,
// so the Makefile prints the set it composes rather than echoing CI's.
func composedBuildTags(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("make", "-s", "print-build-tags", "GO_BUILD_TAGS=")
	cmd.Dir = repoRoot(t)
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "GO_BUILD_TAGS=")
	})
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("make -s print-build-tags: %v\n%s", err, errOut.String())
	}
	return out.String()
}

func TestCIWorkflowMasksForgeCredentialsFirst(t *testing.T) {
	for _, p := range maskProblems(readLogWorkflow(t)) {
		t.Error(p)
	}
}

func TestMaskProblems_CanFail(t *testing.T) {
	const mask = `
      - name: Mask the forge credentials
        run: |
          for v in GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0; do
            echo "::add-mask::${!v}"
            case $v in *://*:*@*) echo "::add-mask::x" ;; esac
          done`
	const checkout = `
      - uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6.1.0`
	const other = `
      - name: Report the workspace
        run: df -h "$GITHUB_WORKSPACE"`
	jobs := func(steps map[string]string) string {
		var b strings.Builder
		b.WriteString("jobs:\n")
		for _, name := range credentialJobs {
			b.WriteString("  " + name + ":\n    steps:" + steps[name] + "\n")
		}
		return b.String()
	}
	good := map[string]string{}
	for _, name := range credentialJobs {
		good[name] = checkout + mask + other
	}
	if p := maskProblems(parseLogWorkflow(t, jobs(good))); len(p) != 0 {
		t.Fatalf("a conforming workflow reported %v", p)
	}
	cases := map[string]string{
		"masking step missing":        checkout + other,
		"masking step not first":      checkout + other + mask,
		"masking step misses a value": checkout + strings.ReplaceAll(mask, " GIT_CONFIG_VALUE_0", "") + other,
		"token in the URL not masked": checkout + strings.ReplaceAll(mask, "*://*:*@*", "*") + other,
	}
	for name, steps := range cases {
		t.Run(name, func(t *testing.T) {
			bad := maps.Clone(good)
			bad["integration-features"] = steps
			if p := maskProblems(parseLogWorkflow(t, jobs(bad))); len(p) == 0 {
				t.Fatal("the gate accepted it")
			}
		})
	}
	if p := maskProblems(parseLogWorkflow(t, jobs(good)+"  new-job:\n    steps:"+checkout+other+"\n")); len(p) == 0 {
		t.Fatal("a job in neither list passed")
	}
	if p := maskProblems(parseLogWorkflow(t, jobs(good)+"  ci-gate:\n    steps:"+checkout+other+"\n")); len(p) != 0 {
		t.Fatalf("an unmasked job in unmaskedJobs reported %v", p)
	}
	delete(good, "image")
	if p := maskProblems(parseLogWorkflow(t, jobs(good))); len(p) == 0 {
		t.Fatal("a workflow with no image job passed")
	}
}

func TestCIWorkflowPrintsNoEnvironment(t *testing.T) {
	for _, p := range envPrintProblems(readLogWorkflow(t)) {
		t.Error(p)
	}
}

func TestEnvPrintProblems_CanFail(t *testing.T) {
	step := func(run string) string {
		return "jobs:\n  image:\n    steps:\n      - name: s\n        run: |\n          " + run + "\n"
	}
	for _, run := range []string{
		`"$RUNNER_TEMP/devcache-env" env >> "$GITHUB_ENV" || true`,
		`make docker-build MODULE_ENV_FILE="$RUNNER_TEMP/gomodauth.env"`,
		`rm -f "$RUNNER_TEMP/gomodauth.env"`,
		`echo "TOOLCHAIN_IMAGE=$(make -s toolchain-image)" >> "$GITHUB_ENV"`,
	} {
		if p := envPrintProblems(parseLogWorkflow(t, step(run))); len(p) != 0 {
			t.Errorf("%q reported %v", run, p)
		}
	}
	for _, run := range []string{
		`env`,
		`env | sort`,
		`true; env > /dev/stderr`,
		`printenv GIT_CONFIG_KEY_0`,
		`set -x`,
		`set -o xtrace`,
		`cat .devcontainer/.env`,
		`cat "$RUNNER_TEMP/gomodauth.env"`,
		`tail "$MODULE_ENV_FILE"`,
	} {
		if p := envPrintProblems(parseLogWorkflow(t, step(run))); len(p) == 0 {
			t.Errorf("%q was accepted", run)
		}
	}
}

func TestCIGoBuildTagsMatchMakefile(t *testing.T) {
	if p := goBuildTagsProblem(readLogWorkflow(t).Env["GO_BUILD_TAGS"], composedBuildTags(t)); p != "" {
		t.Fatal(p + ". Copy the Makefile's set into ci.yml's env: GO_BUILD_TAGS replaces the composed set in every CI stage, so a tag missing from it is untested and uncounted in CI.")
	}
}

func TestGoBuildTagsProblem_CanFail(t *testing.T) {
	if p := goBuildTagsProblem("a b c", "a b c\n"); p != "" {
		t.Fatalf("equal sets reported %q", p)
	}
	for name, c := range map[string][2]string{
		"a tag missing":  {"a b", "a b c"},
		"a tag extra":    {"a b c d", "a b c"},
		"order differs":  {"b a c", "a b c"},
		"ci empty":       {"", "a b c"},
		"makefile empty": {"a b c", ""},
		"a tag misspelt": {"a b cc", "a b c"},
	} {
		if goBuildTagsProblem(c[0], c[1]) == "" {
			t.Errorf("%s: accepted", name)
		}
	}
}
