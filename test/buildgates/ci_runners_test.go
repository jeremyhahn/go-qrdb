// ci_runners_test.go is the gate over where the workflows run (DS-7377).
//
// Every CI job runs on a GitHub-hosted ubuntu-latest runner: an ephemeral VM
// with 4 vCPUs, 16 GB of memory and about 14 GB of free disk, exclusive to
// one job, killed at 6 hours. The self-hosted machinery the forge's runners
// needed -- the bigdisk, batch and ci-gate labels, the forge registry and its
// RELEASE_REGISTRY credential, the named cache volumes and their trim, the
// stale-stack reaper and the schedule guard -- is gone, and this gate keeps
// each piece from coming back. It fails when:
//
//   - a job's runs-on is not the literal `ubuntu-latest`, outside
//     code-review.yml, whose review jobs keep their self-hosted hosts;
//   - an `if:` still splits on github.server_url, outside the jobs that speak
//     the Gitea API (giteaOnlyJobs) and batch.yml, whose splits are left to
//     their owner;
//   - a workflow names a removed script, label or credential, or a removed
//     script is back in scripts/;
//   - a job has no timeout-minutes, or one over jobMinutesCeiling;
//   - an actions/cache entry is saved on any event but a push to main, or by
//     the combined actions/cache action, which saves on every event;
//   - a workflow writes a GOMAXPROCS or go -p count instead of letting
//     scripts/in-toolchain-image.sh derive it from the runner.
//
// The discovery floors keep an empty parse from passing: every other
// assertion is inside a loop, and an empty loop reports success.
package buildgates

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
	// jobMinutesCeiling is the most minutes any job may be given: a hosted
	// job is killed at 360, and a limit under that names the runaway job
	// rather than letting the runner cancel it unnamed.
	jobMinutesCeiling = 350
	// hostedRunner is the one runs-on a CI job may name.
	hostedRunner = "ubuntu-latest"
	// ciPublishExpr is when a run publishes: a push to main, and nothing else.
	ciPublishExpr = "${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}"
	// cachePublishTerm is the conjunct every cache save's `if:` carries.
	cachePublishTerm = "env.CI_PUBLISH == 'true'"
	// cacheSaveAction and cacheAction are the split and the combined cache
	// actions; the combined one saves in its post step on every event.
	cacheSaveAction = "actions/cache/save@"
	cacheAction     = "actions/cache@"

	// Floors, well under today's counts (7 workflows, 50 jobs).
	runnerWorkflowFloor = 6
	runnerJobFloor      = 30
)

// selfHostedWorkflows keep self-hosted runners: the review jobs run where the
// reviewer's credentials are mounted (conventions/sdlc.md).
var selfHostedWorkflows = map[string]bool{"code-review.yml": true}

// giteaOnlyJobs speak the Gitea API (its issue queue, its merge endpoint) and
// so do not run on github.com: the server_url guard is what keeps them off.
var giteaOnlyJobs = map[string]map[string]bool{
	"nightly.yml": {"signal": true},
}

// serverURLExemptWorkflows keep their server_url splits for now: batch.yml's
// failure-notification steps are its owner's to collapse.
var serverURLExemptWorkflows = map[string]bool{"batch.yml": true}

// removedMachinery is every name the self-hosted CI used that no workflow
// may carry again.
var removedMachinery = []string{
	"ci-registry.sh", "ci-image-cache.sh", "trim-ci-caches", "reap-stale-ci-stacks",
	"schedule-guard", "RELEASE_REGISTRY", "bigdisk", "docker volume",
}

// removedScripts are the scripts that implemented it.
var removedScripts = []string{
	"ci-registry.sh", "ci-image-cache.sh", "trim-ci-caches.sh",
	"trim-ci-caches_test.sh", "reap-stale-ci-stacks.sh",
}

// hardcodedParallelism matches a written GOMAXPROCS or go -p count.
var hardcodedParallelism = regexp.MustCompile(`GOMAXPROCS\s*[=:]\s*['"]?[0-9]|(^|\s)-p[= ]?[0-9]`)

// runnerStep and runnerJob are the slices of a workflow this gate reads.
type runnerStep struct {
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}

type runnerJob struct {
	RunsOn   yaml.Node `yaml:"runs-on"`
	If       string    `yaml:"if"`
	Timeout  yaml.Node `yaml:"timeout-minutes"`
	Strategy struct {
		Matrix yaml.Node `yaml:"matrix"`
	} `yaml:"strategy"`
	Env   map[string]string `yaml:"env"`
	Steps []runnerStep      `yaml:"steps"`
}

type runnerWorkflow struct {
	Env  map[string]string    `yaml:"env"`
	Jobs map[string]runnerJob `yaml:"jobs"`
}

// workflowFiles reads every workflow under .github/workflows, keyed by name.
func workflowFiles(t *testing.T) map[string][]byte {
	t.Helper()
	dir := filepath.Join(repoRoot(t), ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || !(strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml")) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = b
	}
	return out
}

// runnerProblems returns every way the workflow named file breaks the hosted
// runner rules, and the number of jobs it checked.
func runnerProblems(file string, body []byte) (problems []string, jobs int) {
	var wf runnerWorkflow
	if err := yaml.Unmarshal(body, &wf); err != nil {
		return []string{file + ": not parseable YAML: " + err.Error()}, 0
	}
	if selfHostedWorkflows[file] {
		return nil, 0
	}
	add := func(format string, a ...string) {
		problems = append(problems, file+": "+strings.Join(append([]string{format}, a...), ""))
	}
	for _, m := range removedMachinery {
		if strings.Contains(string(body), m) {
			add("names `" + m + "`, part of the self-hosted CI DS-7377 removed")
		}
	}
	usesCache := false
	names := slices.Sorted(func(yield func(string) bool) {
		for n := range wf.Jobs {
			if !yield(n) {
				return
			}
		}
	})
	for _, name := range names {
		job := wf.Jobs[name]
		jobs++
		if job.RunsOn.Kind != yaml.ScalarNode || job.RunsOn.Value != hostedRunner {
			add("job "+name+" runs on ", nodeText(job.RunsOn), ", not the literal "+hostedRunner)
		}
		problems = append(problems, timeoutProblems(file, name, job)...)
		if !serverURLExemptWorkflows[file] && !giteaOnlyJobs[file][name] {
			if strings.Contains(job.If, "server_url") {
				add("job " + name + "'s if: splits on github.server_url; every job runs on github.com")
			}
			for _, s := range job.Steps {
				if strings.Contains(s.If, "server_url") {
					add("job " + name + " has a step whose if: splits on github.server_url; every step runs on github.com")
				}
			}
		}
		for _, s := range job.Steps {
			switch {
			case strings.HasPrefix(s.Uses, cacheAction):
				add("job " + name + " uses the combined actions/cache, which saves on every event; restore and save apart, the save on CI_PUBLISH alone")
			case strings.HasPrefix(s.Uses, cacheSaveAction):
				usesCache = true
				if !strings.Contains(s.If, cachePublishTerm) {
					add("job "+name+" saves a cache under if: ", strconv.Quote(s.If), "; only a push to main saves one ("+cachePublishTerm+")")
				}
			}
			if hardcodedParallelism.MatchString(s.Run) {
				add("job " + name + " writes a GOMAXPROCS or -p count; scripts/in-toolchain-image.sh derives it from the runner")
			}
			for k, v := range s.Env {
				if k == "GOMAXPROCS" || hardcodedParallelism.MatchString(k+"="+v) {
					add("job " + name + " sets " + k + " in a step's env; the stage derives its parallelism")
				}
			}
		}
		for k := range job.Env {
			if k == "GOMAXPROCS" {
				add("job " + name + " sets GOMAXPROCS; the stage derives it from the runner")
			}
		}
	}
	if usesCache && wf.Env["CI_PUBLISH"] != ciPublishExpr {
		add("env.CI_PUBLISH is ", strconv.Quote(wf.Env["CI_PUBLISH"]), ", not ", ciPublishExpr)
	}
	return problems, jobs
}

// timeoutProblems checks one job's limit: a literal number, or a matrix
// variable every one of whose values is, at most jobMinutesCeiling.
func timeoutProblems(file, name string, job runnerJob) []string {
	where := file + ": job " + name
	if job.Timeout.Kind == 0 {
		return []string{where + " has no timeout-minutes; the hosted default of 360 is over the ceiling of " + strconv.Itoa(jobMinutesCeiling)}
	}
	var values []string
	v := strings.TrimSpace(job.Timeout.Value)
	if key, ok := matrixRef(v); ok {
		values = matrixValues(job.Strategy.Matrix, key)
		if len(values) == 0 {
			return []string{where + "'s timeout-minutes is " + v + ", which no matrix entry sets"}
		}
	} else {
		values = []string{v}
	}
	var out []string
	for _, s := range values {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 || n > jobMinutesCeiling {
			out = append(out, where+"'s timeout-minutes "+strconv.Quote(s)+" is not a number of minutes from 1 to "+strconv.Itoa(jobMinutesCeiling))
		}
	}
	return out
}

// matrixRef returns K for the expression `${{ matrix.K }}`.
func matrixRef(v string) (string, bool) {
	inner, ok := strings.CutPrefix(v, "${{")
	if !ok {
		return "", false
	}
	inner, ok = strings.CutSuffix(inner, "}}")
	if !ok {
		return "", false
	}
	return strings.CutPrefix(strings.TrimSpace(inner), "matrix.")
}

// matrixValues returns every value the matrix gives key: its list, or each
// include entry's.
func matrixValues(m yaml.Node, key string) []string {
	var out []string
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i].Value, m.Content[i+1]
		switch {
		case k == key && v.Kind == yaml.SequenceNode:
			for _, e := range v.Content {
				out = append(out, e.Value)
			}
		case k == "include" && v.Kind == yaml.SequenceNode:
			for _, entry := range v.Content {
				for j := 0; j+1 < len(entry.Content); j += 2 {
					if entry.Content[j].Value == key {
						out = append(out, entry.Content[j+1].Value)
					}
				}
			}
		}
	}
	return out
}

// nodeText renders a runs-on node for a message.
func nodeText(n yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return strconv.Quote(n.Value)
	}
	b, _ := yaml.Marshal(&n)
	return strconv.Quote(strings.TrimSpace(string(b)))
}

func TestCIRunsOnHostedRunners(t *testing.T) {
	files := workflowFiles(t)
	if len(files) < runnerWorkflowFloor {
		t.Fatalf("found %d workflows, under the floor of %d", len(files), runnerWorkflowFloor)
	}
	total := 0
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for n := range files {
			if !yield(n) {
				return
			}
		}
	}) {
		problems, jobs := runnerProblems(name, files[name])
		total += jobs
		for _, p := range problems {
			t.Error(p)
		}
	}
	if total < runnerJobFloor {
		t.Errorf("checked %d jobs, under the floor of %d", total, runnerJobFloor)
	}
}

func TestCIRemovedScriptsStayRemoved(t *testing.T) {
	for _, s := range removedScripts {
		if _, err := os.Stat(filepath.Join(repoRoot(t), "scripts", s)); err == nil {
			t.Errorf("scripts/%s is back; it served the self-hosted CI DS-7377 removed", s)
		}
	}
}

func TestCIServerURLAbsentFromCI(t *testing.T) {
	if strings.Contains(readCIBody(t), "server_url") {
		t.Error("ci.yml names github.server_url; it runs on github.com alone, so nothing in it depends on the host")
	}
}

// runnerFixture is a workflow that passes every rule.
const runnerFixture = `env:
  CI_PUBLISH: ${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    steps:
      - uses: actions/cache/restore@0000000000000000000000000000000000000000 # v6
      - run: make build
      - if: ${{ !cancelled() && env.CI_PUBLISH == 'true' }}
        uses: actions/cache/save@0000000000000000000000000000000000000000 # v6
  legs:
    runs-on: ubuntu-latest
    timeout-minutes: ${{ matrix.minutes }}
    strategy:
      matrix:
        include:
          - leg: a
            minutes: 120
          - leg: b
            minutes: 350
    steps:
      - run: make leg
`

func TestRunnerProblems_FixtureIsValid(t *testing.T) {
	problems, jobs := runnerProblems("ci.yml", []byte(runnerFixture))
	if len(problems) != 0 || jobs != 2 {
		t.Fatalf("the fixture must pass with 2 jobs, got %d jobs: %v", jobs, problems)
	}
}

// TestRunnerProblems_CanFail mutates the fixture once per rule: a gate that
// cannot go red proves nothing.
func TestRunnerProblems_CanFail(t *testing.T) {
	cases := map[string]struct {
		file, old, new, want string
	}{
		"a self-hosted label":           {"ci.yml", "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: bigdisk", "not the literal ubuntu-latest"},
		"a runner list":                 {"ci.yml", "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: [self-hosted, linux]", "not the literal ubuntu-latest"},
		"an expression":                 {"ci.yml", "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: ${{ github.server_url == 'https://github.com' && 'ubuntu-latest' || 'batch' }}", "not the literal ubuntu-latest"},
		"a job server_url split":        {"ci.yml", "    timeout-minutes: 30\n", "    timeout-minutes: 30\n    if: github.server_url != 'https://github.com'\n", "splits on github.server_url"},
		"a step server_url split":       {"nightly.yml", "      - run: make build", "      - if: github.server_url == 'https://github.com'\n        run: make build", "splits on github.server_url"},
		"the registry credential":       {"ci.yml", "make build", "make build RELEASE_REGISTRY=x", "RELEASE_REGISTRY"},
		"the cache trim":                {"ci.yml", "make build", "scripts/trim-ci-caches.sh 32", "trim-ci-caches"},
		"a cache volume":                {"ci.yml", "make build", "docker volume create qrdb-cache", "docker volume"},
		"the schedule guard":            {"ci.yml", "make build", "echo schedule-guard", "schedule-guard"},
		"the reaper":                    {"ci.yml", "make build", "scripts/reap-stale-ci-stacks.sh", "reap-stale-ci-stacks"},
		"no timeout":                    {"ci.yml", "    timeout-minutes: 30\n", "", "has no timeout-minutes"},
		"a timeout over the ceiling":    {"ci.yml", "timeout-minutes: 30", "timeout-minutes: 351", "not a number of minutes from 1 to 350"},
		"a matrix leg over the ceiling": {"ci.yml", "minutes: 350", "minutes: 360", "not a number of minutes from 1 to 350"},
		"a matrix limit nothing sets":   {"ci.yml", "timeout-minutes: ${{ matrix.minutes }}", "timeout-minutes: ${{ matrix.limit }}", "which no matrix entry sets"},
		"a cache saved on every event":  {"ci.yml", "${{ !cancelled() && env.CI_PUBLISH == 'true' }}", "${{ !cancelled() }}", "only a push to main saves one"},
		"the combined cache action":     {"ci.yml", "actions/cache/restore@", "actions/cache@", "combined actions/cache"},
		"a publish flag on every push":  {"ci.yml", "github.event_name == 'push' && github.ref == 'refs/heads/main'", "github.event_name == 'push'", "env.CI_PUBLISH is"},
		"a written GOMAXPROCS":          {"ci.yml", "make leg", "GOMAXPROCS=4 make leg", "GOMAXPROCS or -p count"},
		"a written -p":                  {"ci.yml", "make leg", "go test -p 4 ./...", "GOMAXPROCS or -p count"},
		"a GOMAXPROCS step env":         {"ci.yml", "      - run: make leg", "      - run: make leg\n        env:\n          GOMAXPROCS: \"8\"", "sets GOMAXPROCS in a step's env"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(runnerFixture, c.old) {
				t.Fatalf("the fixture lacks %q", c.old)
			}
			body := strings.Replace(runnerFixture, c.old, c.new, 1)
			problems, _ := runnerProblems(c.file, []byte(body))
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, c.want) }) {
				t.Fatalf("want a problem containing %q, got %v", c.want, problems)
			}
		})
	}
}

// TestRunnerProblems_Exemptions holds the three exemptions to their scope: a
// review workflow's self-hosted label, a Gitea-API job's server_url guard,
// and batch.yml's splits.
func TestRunnerProblems_Exemptions(t *testing.T) {
	selfHosted := strings.Replace(runnerFixture, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: codex", 1)
	if p, _ := runnerProblems("code-review.yml", []byte(selfHosted)); len(p) != 0 {
		t.Errorf("code-review.yml keeps its self-hosted labels, got %v", p)
	}
	if p, _ := runnerProblems("ci.yml", []byte(selfHosted)); len(p) == 0 {
		t.Error("ci.yml's self-hosted label passed; the exemption is code-review.yml's alone")
	}
	guarded := strings.Replace(runnerFixture, "  build:\n    runs-on: ubuntu-latest", "  signal:\n    if: always() && github.server_url != 'https://github.com'\n    runs-on: ubuntu-latest", 1)
	if p, _ := runnerProblems("nightly.yml", []byte(guarded)); len(p) != 0 {
		t.Errorf("nightly.yml's signal job speaks the Gitea API and keeps its guard, got %v", p)
	}
	if p, _ := runnerProblems("release.yml", []byte(guarded)); len(p) == 0 {
		t.Error("a signal job's guard in release.yml passed; the exemption is per workflow and job")
	}
	if p, _ := runnerProblems("batch.yml", []byte(guarded)); len(p) != 0 {
		t.Errorf("batch.yml keeps its splits for its owner, got %v", p)
	}
}

func TestMatrixRef(t *testing.T) {
	for in, want := range map[string]string{"${{ matrix.minutes }}": "minutes", "${{matrix.x}}": "x"} {
		if got, ok := matrixRef(in); !ok || got != want {
			t.Errorf("matrixRef(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"120", "${{ env.MINUTES }}", "matrix.minutes"} {
		if _, ok := matrixRef(in); ok {
			t.Errorf("matrixRef(%q) took it for a matrix variable", in)
		}
	}
}
