// ci_runners_test.go is the gate over where the workflows run (DS-7377,
// CA ruling 617).
//
// ci.yml's jobs run on the self-hosted pools for a push to main and a
// same-repository pull request, and on a GitHub-hosted ubuntu-latest runner
// otherwise: a fork's pull request never reaches a pool host. A job that
// needs Docker goes to the bigdisk-docker pool, a Docker-free job to the
// bigdisk pool, which has no Docker daemon. Every other workflow runs on
// ubuntu-latest. The self-hosted machinery the forge's runners needed -- the
// batch and ci-gate labels, the forge registry and its RELEASE_REGISTRY
// credential, the named cache volumes and their trim, the stale-stack reaper
// and the schedule guard -- is gone, and this gate keeps each piece from
// coming back. It fails when:
//
//   - a ci.yml job's runs-on is not exactly dockerRunner or bigdiskRunner
//     (poolRunners), or another workflow's is not the literal
//     `ubuntu-latest`, outside code-review.yml, whose review jobs keep their
//     self-hosted hosts;
//   - a job on bigdiskRunner has a container, a service or a Docker step the
//     pool could reach (dockerNeeds), a job on dockerRunner needs no Docker,
//     or a pool workflow triggers on pull_request_target;
//   - a step that wipes a hosted runner's disk (sudo rm, docker prune) may
//     run on a pool host, whose images and directories other jobs share;
//   - ci.yml's playwrightJob leaves a credential in its checkout or runs an
//     apt install on the pool (Company Agent m2396);
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
	// hostedRunner is the one runs-on a job outside poolWorkflow may name,
	// and every pool expression's fallback.
	hostedRunner = "ubuntu-latest"
	// poolWorkflow is the workflow whose jobs run on the self-hosted pools
	// (CA ruling 617); every one of its jobs carries a poolRunners expression.
	poolWorkflow = "ci.yml"
	// bigdiskLabel is the Docker-free pool's label (DS-8233): no Docker
	// daemon, so only a job that needs none may reach it. Its Docker pool's
	// label, bigdisk-docker, carries it as a prefix.
	bigdiskLabel = "bigdisk"
	// poolSplit is the condition both pool expressions share: a push to main
	// or a pull request whose head is this repository. Anything else, a
	// fork's pull request, a schedule or a dispatch, gets ubuntu-latest.
	poolSplit = `((github.event_name == 'push' && github.ref == 'refs/heads/main') || (github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository))`
	// bigdiskRunner is a Docker-free job's runs-on, character for character.
	bigdiskRunner = `${{ ` + poolSplit + ` && fromJSON('["self-hosted","linux","x64","bigdisk"]') || 'ubuntu-latest' }}`
	// dockerRunner is the runs-on of a job that needs Docker, character for
	// character.
	dockerRunner = `${{ ` + poolSplit + ` && fromJSON('["self-hosted","linux","x64","bigdisk-docker"]') || 'ubuntu-latest' }}`
	// hostedStepTerm is the conjunct a step carries in its `if:` so that it
	// never runs on a pool host: a Docker step of a bigdiskRunner job, and a
	// step that wipes a hosted runner's disk in any pool job.
	hostedStepTerm = "runner.environment == 'github-hosted'"
	// playwrightJob runs its make target natively on the bigdisk pool
	// (Company Agent m2396, 2026-11-10), so it is held to leaving no
	// credential in its checkout and installing no apt package there.
	playwrightJob = "test-typescript"
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
	// Pool floors, under ci.yml's counts (7 Docker-free jobs, 11 Docker jobs).
	bigdiskJobFloor = 5
	dockerJobFloor  = 8
)

// poolRunners are poolWorkflow's two runs-on expressions.
var poolRunners = []string{bigdiskRunner, dockerRunner}

// selfHostedWorkflows keep self-hosted runners: the review jobs run where the
// reviewer's credentials are mounted (conventions/sdlc.md).
var selfHostedWorkflows = map[string]bool{"code-review.yml": true}

// dockerStep matches a step that needs Docker: the docker or docker-compose
// CLI, a docker/* action, a make target named docker-* or the compose-stack
// integration-test-* and integration-down*, or the image wrappers
// (scripts/in-toolchain-image.sh, scripts/ci-image.sh). A job name such as
// docker-build, which ci-gate reads the result of, is not a Docker use, so
// the CLI matches only as a command word.
var dockerStep = regexp.MustCompile(`(^|[^\w.-])docker(-compose)?(\s|$)|(^|[^\w.-])docker/|\bmake\b[^\n]*[\s'"](docker-|integration-test|integration-down)|in-toolchain-image\.sh|ci-image\.sh`)

// hostWipeStep matches a step that frees a hosted runner's disk: a sudo rm
// of the preinstalled SDKs, or a docker prune that would delete the images
// other jobs on a shared pool host hold.
var hostWipeStep = regexp.MustCompile(`\bsudo\s+rm\b|\bdocker\s+(image\s+|system\s+|container\s+|builder\s+)?prune\b`)

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
	With map[string]string `yaml:"with"`
}

type runnerJob struct {
	RunsOn    yaml.Node `yaml:"runs-on"`
	Container yaml.Node `yaml:"container"`
	Services  yaml.Node `yaml:"services"`
	If        string    `yaml:"if"`
	Timeout   yaml.Node `yaml:"timeout-minutes"`
	Strategy  struct {
		Matrix yaml.Node `yaml:"matrix"`
	} `yaml:"strategy"`
	Env   map[string]string `yaml:"env"`
	Steps []runnerStep      `yaml:"steps"`
}

type runnerWorkflow struct {
	On   yaml.Node            `yaml:"on"`
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

// runnerProblems returns every way the workflow named file breaks the runner
// rules, and the number of jobs it checked.
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
	pool := file == poolWorkflow
	for _, m := range removedMachinery {
		// The pool workflow names the label in its runs-on and its comments;
		// the per-job runs-on check below bounds its use.
		if m == bigdiskLabel && pool {
			continue
		}
		if strings.Contains(string(body), m) {
			add("names `" + m + "`, part of the self-hosted CI DS-7377 removed")
		}
	}
	if pool && slices.Contains(triggerNames(wf.On), "pull_request_target") {
		add("the workflow triggers on pull_request_target, which would run a fork's code on a pool host with this repository's secrets")
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
		runsOn := ""
		if job.RunsOn.Kind == yaml.ScalarNode {
			runsOn = job.RunsOn.Value
		}
		switch {
		case pool && runsOn == bigdiskRunner:
			for _, p := range dockerNeeds(job) {
				add("job " + name + " runs on the Docker-free bigdisk pool (bigdiskRunner), and " + p + "; a job that needs Docker carries dockerRunner")
			}
		case pool && runsOn == dockerRunner:
			if len(dockerNeeds(job)) == 0 {
				add("job " + name + " runs on the bigdisk-docker pool (dockerRunner) but needs no Docker; a Docker-free job carries bigdiskRunner")
			}
		case pool:
			add("job "+name+" runs on ", nodeText(job.RunsOn), ", not exactly bigdiskRunner (Docker-free) or dockerRunner (Docker), the pool for a push to main and a same-repository pull request and "+hostedRunner+" for a fork (ruling 617)")
		case runsOn == hostedRunner:
		default:
			add("job "+name+" runs on ", nodeText(job.RunsOn), ", not the literal "+hostedRunner)
		}
		if pool {
			for _, s := range job.Steps {
				if hostWipeStep.MatchString(s.Run) && !strings.Contains(s.If, hostedStepTerm) {
					add("job "+name+" has a step that wipes a hosted runner's disk under if: ", strconv.Quote(s.If), ", without "+hostedStepTerm+"; on a pool host it deletes what other jobs share")
				}
			}
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

// dockerNeeds is every way a job needs Docker on a pool host: a container,
// services, or a step that needs Docker (dockerStep) and is not gated to the
// hosted runner (hostedStepTerm). A bigdiskRunner job must have none, since
// the bigdisk pool has no Docker daemon; a dockerRunner job must have one.
func dockerNeeds(job runnerJob) (needs []string) {
	if job.Container.Kind != 0 {
		needs = append(needs, "it has a container:, which needs Docker")
	}
	if job.Services.Kind != 0 {
		needs = append(needs, "it has services:, which need Docker")
	}
	for _, s := range job.Steps {
		if dockerStep.MatchString(s.Uses+"\n"+s.Run) && !strings.Contains(s.If, hostedStepTerm) {
			needs = append(needs, "a step that needs Docker runs under if: "+strconv.Quote(s.If)+", without "+hostedStepTerm)
		}
	}
	return needs
}

// triggerNames is the events of a workflow's `on:`, in any of its three
// forms: one name, a list of names, or a mapping keyed by name.
func triggerNames(on yaml.Node) (names []string) {
	switch on.Kind {
	case yaml.ScalarNode:
		names = append(names, on.Value)
	case yaml.SequenceNode:
		for _, c := range on.Content {
			names = append(names, c.Value)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(on.Content); i += 2 {
			names = append(names, on.Content[i].Value)
		}
	}
	return names
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

// TestCIRunners runs runnerProblems over every workflow, and holds ci.yml to
// using both pools (ruling 617): a floor of jobs on each expression, so that
// a regression to ubuntu-latest everywhere or to one pool fails here too.

func TestCIRunners(t *testing.T) {
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
	var wf runnerWorkflow
	if err := yaml.Unmarshal(files[poolWorkflow], &wf); err != nil {
		t.Fatalf("parse %s: %v", poolWorkflow, err)
	}
	bigdisk, docker := 0, 0
	for _, job := range wf.Jobs {
		switch job.RunsOn.Value {
		case bigdiskRunner:
			bigdisk++
		case dockerRunner:
			docker++
		}
	}
	if bigdisk < bigdiskJobFloor || docker < dockerJobFloor {
		t.Errorf("%s has %d jobs on bigdiskRunner and %d on dockerRunner, under the floors of %d and %d", poolWorkflow, bigdisk, docker, bigdiskJobFloor, dockerJobFloor)
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

// hostedFile is the workflow name runnerFixture is checked as: one held to
// the literal ubuntu-latest.
const hostedFile = "release.yml"

// runnerFixture is a hosted workflow that passes every rule.
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
	problems, jobs := runnerProblems(hostedFile, []byte(runnerFixture))
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
		"a self-hosted label":           {hostedFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: bigdisk", "not the literal ubuntu-latest"},
		"a runner list":                 {hostedFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: [self-hosted, linux]", "not the literal ubuntu-latest"},
		"an expression":                 {hostedFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: ${{ github.server_url == 'https://github.com' && 'ubuntu-latest' || 'batch' }}", "not the literal ubuntu-latest"},
		"a job server_url split":        {hostedFile, "    timeout-minutes: 30\n", "    timeout-minutes: 30\n    if: github.server_url != 'https://github.com'\n", "splits on github.server_url"},
		"a step server_url split":       {"nightly.yml", "      - run: make build", "      - if: github.server_url == 'https://github.com'\n        run: make build", "splits on github.server_url"},
		"the registry credential":       {hostedFile, "make build", "make build RELEASE_REGISTRY=x", "RELEASE_REGISTRY"},
		"the cache trim":                {hostedFile, "make build", "scripts/trim-ci-caches.sh 32", "trim-ci-caches"},
		"a cache volume":                {hostedFile, "make build", "docker volume create qrdb-cache", "docker volume"},
		"the schedule guard":            {hostedFile, "make build", "echo schedule-guard", "schedule-guard"},
		"the reaper":                    {hostedFile, "make build", "scripts/reap-stale-ci-stacks.sh", "reap-stale-ci-stacks"},
		"no timeout":                    {hostedFile, "    timeout-minutes: 30\n", "", "has no timeout-minutes"},
		"a timeout over the ceiling":    {hostedFile, "timeout-minutes: 30", "timeout-minutes: 351", "not a number of minutes from 1 to 350"},
		"a matrix leg over the ceiling": {hostedFile, "minutes: 350", "minutes: 360", "not a number of minutes from 1 to 350"},
		"a matrix limit nothing sets":   {hostedFile, "timeout-minutes: ${{ matrix.minutes }}", "timeout-minutes: ${{ matrix.limit }}", "which no matrix entry sets"},
		"a cache saved on every event":  {hostedFile, "${{ !cancelled() && env.CI_PUBLISH == 'true' }}", "${{ !cancelled() }}", "only a push to main saves one"},
		"the combined cache action":     {hostedFile, "actions/cache/restore@", "actions/cache@", "combined actions/cache"},
		"a publish flag on every push":  {hostedFile, "github.event_name == 'push' && github.ref == 'refs/heads/main'", "github.event_name == 'push'", "env.CI_PUBLISH is"},
		"a written GOMAXPROCS":          {hostedFile, "make leg", "GOMAXPROCS=4 make leg", "GOMAXPROCS or -p count"},
		"a written -p":                  {hostedFile, "make leg", "go test -p 4 ./...", "GOMAXPROCS or -p count"},
		"a GOMAXPROCS step env":         {hostedFile, "      - run: make leg", "      - run: make leg\n        env:\n          GOMAXPROCS: \"8\"", "sets GOMAXPROCS in a step's env"},
		"the hosted runner in ci.yml":   {poolWorkflow, "", "", "not exactly bigdiskRunner (Docker-free) or dockerRunner (Docker)"},
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

// bigdiskStepProblems reports what in playwrightJob's steps is unsafe on a
// runner that outlives the job and forbids sudo: a checkout that leaves the
// workflow token in the checkout's git config, and a `make test-typescript`
// that does not clear PLAYWRIGHT_INSTALL_FLAGS off the hosted runner (the
// Makefile's default, `--with-deps`, installs apt packages through sudo).
func bigdiskStepProblems(job runnerJob) []string {
	var problems []string
	guarded := false
	for i, s := range job.Steps {
		if strings.HasPrefix(s.Uses, "actions/checkout@") && s.With["persist-credentials"] != "false" {
			problems = append(problems, playwrightJob+": step "+strconv.Itoa(i)+" checks out without persist-credentials: false; the token stays in the checkout's git config")
		}
		if runsGuardedMake(s.Run) {
			guarded = true
		}
	}
	if !guarded {
		problems = append(problems, playwrightJob+": no step runs make "+playwrightJob+" with PLAYWRIGHT_INSTALL_FLAGS cleared when RUNNER_ENVIRONMENT is not github-hosted")
	}
	return problems
}

// runnerGuard is the approved block that clears PLAYWRIGHT_INSTALL_FLAGS off
// the hosted runner, line for line after each line's leading whitespace.
var runnerGuard = []string{
	`if [ "${RUNNER_ENVIRONMENT:-}" != github-hosted ]; then`,
	`export PLAYWRIGHT_INSTALL_FLAGS=`,
	`fi`,
}

// runsGuardedMake reports whether script holds runnerGuard as consecutive
// lines and, on a later line, `make test-typescript`.
func runsGuardedMake(script string) bool {
	lines := strings.Split(script, "\n")
	for i := range lines {
		lines[i] = strings.TrimLeft(lines[i], " \t")
	}
	for i := 0; i+len(runnerGuard) <= len(lines); i++ {
		if !slices.Equal(lines[i:i+len(runnerGuard)], runnerGuard) {
			continue
		}
		for _, l := range lines[i+len(runnerGuard):] {
			if l == "make "+playwrightJob {
				return true
			}
		}
	}
	return false
}

// makeRecipe returns the recipe lines of target in a Makefile's text.
func makeRecipe(makefile, target string) []string {
	var recipe []string
	in := false
	for _, line := range strings.Split(makefile, "\n") {
		switch {
		case strings.HasPrefix(line, target+":"):
			in = true
		case in && strings.HasPrefix(line, "\t"):
			recipe = append(recipe, line)
		case in:
			return recipe
		}
	}
	return recipe
}

func TestCIBigdiskJobLeavesNoCredentialAndRunsNoApt(t *testing.T) {
	var wf struct {
		Jobs map[string]runnerJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(workflowFiles(t)["ci.yml"], &wf); err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}
	job, ok := wf.Jobs[playwrightJob]
	if !ok {
		t.Fatalf("ci.yml has no job %s", playwrightJob)
	}
	for _, p := range bigdiskStepProblems(job) {
		t.Error(p)
	}

	b, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	recipe := makeRecipe(string(b), playwrightJob)
	if len(recipe) == 0 {
		t.Fatalf("Makefile has no recipe for %s", playwrightJob)
	}
	const install = "npx playwright install $(PLAYWRIGHT_INSTALL_FLAGS) chromium"
	installs := false
	for _, line := range recipe {
		if strings.Contains(line, "--with-deps") {
			t.Errorf("Makefile's %s recipe holds --with-deps outside PLAYWRIGHT_INSTALL_FLAGS: %q", playwrightJob, line)
		}
		if strings.Contains(line, install) {
			installs = true
		}
	}
	if !installs {
		t.Errorf("Makefile's %s recipe has no line %q", playwrightJob, install)
	}
}

func TestBigdiskStepProblems_CanFail(t *testing.T) {
	const guard = "          if [ \"${RUNNER_ENVIRONMENT:-}\" != github-hosted ]; then\n            export PLAYWRIGHT_INSTALL_FLAGS=\n          fi\n"
	const checkout = "      - uses: actions/checkout@0000000000000000000000000000000000000000\n"
	const safe = "        with:\n          persist-credentials: false\n"
	guarded := "      - run: |\n" + guard + "          make " + playwrightJob + "\n"
	cases := []struct {
		name  string
		steps string
		want  bool
	}{
		{"valid", checkout + safe + guarded, false},
		{"checkout with no with", checkout + guarded, true},
		{"checkout with persist-credentials: true", checkout + "        with:\n          persist-credentials: true\n" + guarded, true},
		{"checkout with another with key only", checkout + "        with:\n          fetch-depth: 0\n" + guarded, true},
		{"make without the guard", checkout + safe + "      - run: make " + playwrightJob + "\n", true},
		{"guard in another step", checkout + safe + "      - run: |\n" + guard + "      - run: make " + playwrightJob + "\n", true},
		{"no make step", checkout + safe, true},
		{"the comparison reversed", checkout + safe + "      - run: |\n" + strings.Replace(guard, " != ", " = ", 1) + "          make " + playwrightJob + "\n", true},
		{"the flag set but not exported", checkout + safe + "      - run: |\n" + strings.Replace(guard, "export ", "", 1) + "          make " + playwrightJob + "\n", true},
		{"make before the guard", checkout + safe + "      - run: |\n          make " + playwrightJob + "\n" + guard, true},
	}
	for _, tc := range cases {
		var job runnerJob
		if err := yaml.Unmarshal([]byte("steps:\n"+tc.steps), &job); err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		p := bigdiskStepProblems(job)
		if got := len(p) != 0; got != tc.want {
			t.Errorf("%s: problems %v, want a problem: %v", tc.name, p, tc.want)
		}
	}
}

// poolFixture is ci.yml as ruling 617 shapes it: a Docker-free job on
// bigdiskRunner, its Docker step gated to the hosted runner and its native
// step to the pool, beside a Docker job on dockerRunner whose disk wipe runs
// on the hosted runner alone.
const poolFixture = `on:
  push:
    branches: [main]
  pull_request:
jobs:
  coverage-fts:
    runs-on: ` + bigdiskRunner + `
    timeout-minutes: 20
    steps:
      - if: runner.environment == 'github-hosted'
        run: docker load -i toolchain.tar
      - if: runner.environment == 'self-hosted'
        run: make coverage-fts
  build:
    runs-on: ` + dockerRunner + `
    timeout-minutes: 30
    steps:
      - name: Free the runner's disk
        if: runner.environment == 'github-hosted'
        run: |
          sudo rm -rf /usr/share/dotnet
          docker image prune -af >/dev/null
      - run: scripts/in-toolchain-image.sh "$IMAGE" "make build"
`

// TestRunnerProblems_Pools is ruling 617's two-pool rule and its edges: a
// ci.yml job carries exactly bigdiskRunner when it needs no Docker and
// exactly dockerRunner when it does, and every other shape (the hosted
// runner, another workflow, another expression, a fork or any push on a
// pool, a label, a Docker step on the Docker-free pool, a Docker-free job on
// the Docker pool, a disk wipe a pool host could run, pull_request_target)
// fails.
func TestRunnerProblems_Pools(t *testing.T) {
	if p, jobs := runnerProblems(poolWorkflow, []byte(poolFixture)); len(p) != 0 || jobs != 2 {
		t.Fatalf("the fixture must pass with 2 jobs, got %d jobs: %v", jobs, p)
	}
	// ci-gate on the pool (m2479) names docker-build as a job whose result it
	// reads; that is not a Docker use.
	gate := strings.Replace(poolFixture, "        run: make coverage-fts", "        run: |\n          check docker-build \"$RESULT_DOCKER_BUILD\" pr\n          echo \"needs.docker-build.result\"", 1)
	if p, _ := runnerProblems(poolWorkflow, []byte(gate)); len(p) != 0 {
		t.Errorf("a step that only names the docker-build job may run on the pool, got %v", p)
	}
	const bigdiskOn = "    runs-on: " + bigdiskRunner
	const dockerOn = "    runs-on: " + dockerRunner
	const sameRepo = " && github.event.pull_request.head.repo.full_name == github.repository"
	const dockerLoad = "      - if: runner.environment == 'github-hosted'\n        run: docker load -i toolchain.tar"
	const wipe = "        if: runner.environment == 'github-hosted'\n        run: |\n          sudo rm"
	const wrapper = "      - run: scripts/in-toolchain-image.sh"
	cases := map[string]struct {
		file, old, new, want string
	}{
		"another workflow's job":              {hostedFile, "", "", "job coverage-fts runs on"},
		"another workflow's Docker job":       {hostedFile, "", "", "job build runs on"},
		"another workflow's label":            {hostedFile, "", "", "names `bigdisk`"},
		"a hosted Docker-free job":            {poolWorkflow, bigdiskOn, "    runs-on: ubuntu-latest", "job coverage-fts runs on"},
		"a hosted Docker job":                 {poolWorkflow, dockerOn, "    runs-on: ubuntu-latest", "job build runs on"},
		"any push":                            {poolWorkflow, "github.event_name == 'push' && github.ref == 'refs/heads/main'", "github.event_name == 'push'", "job coverage-fts runs on"},
		"a fork's pull request":               {poolWorkflow, sameRepo, "", "job coverage-fts runs on"},
		"a fork's pull request, Docker pool":  {poolWorkflow, dockerOn, strings.Replace(dockerOn, sameRepo, "", 1), "job build runs on"},
		"pull_request_target routed":          {poolWorkflow, "github.event_name == 'pull_request'", "github.event_name == 'pull_request_target'", "job coverage-fts runs on"},
		"no hosted fallback":                  {poolWorkflow, " || 'ubuntu-latest'", "", "job coverage-fts runs on"},
		"no hosted fallback, Docker pool":     {poolWorkflow, dockerOn, strings.Replace(dockerOn, " || 'ubuntu-latest'", "", 1), "job build runs on"},
		"the label list":                      {poolWorkflow, bigdiskOn, "    runs-on: [self-hosted, linux, x64, bigdisk]", "job coverage-fts runs on"},
		"the Docker label list":               {poolWorkflow, dockerOn, "    runs-on: [self-hosted, linux, x64, bigdisk-docker]", "job build runs on"},
		"the bare label":                      {poolWorkflow, bigdiskOn, "    runs-on: bigdisk", "job coverage-fts runs on"},
		"another pool":                        {poolWorkflow, `"bigdisk"]`, `"batch"]`, "job coverage-fts runs on"},
		"a Docker job on bigdisk":             {poolWorkflow, dockerOn, bigdiskOn, "job build runs on the Docker-free bigdisk pool"},
		"a Docker-free job on bigdisk-docker": {poolWorkflow, bigdiskOn, dockerOn, "job coverage-fts runs on the bigdisk-docker pool (dockerRunner) but needs no Docker"},
		"a disk wipe on a pool host":          {poolWorkflow, wipe, "        run: |\n          sudo rm", "wipes a hosted runner's disk"},
		"a docker prune on a pool host":       {poolWorkflow, wrapper, "      - run: docker system prune -af\n" + wrapper, "wipes a hosted runner's disk"},
		"a Docker step on the pool":           {poolWorkflow, dockerLoad, "      - run: docker load -i toolchain.tar", "a step that needs Docker"},
		"a Docker step, pool only":            {poolWorkflow, dockerLoad, "      - if: runner.environment == 'self-hosted'\n        run: docker load -i toolchain.tar", "a step that needs Docker"},
		"the image wrapper":                   {poolWorkflow, "        run: make coverage-fts", "        run: scripts/in-toolchain-image.sh \"$IMAGE\" \"make coverage-fts\"", "a step that needs Docker"},
		"a docker action":                     {poolWorkflow, "        run: make coverage-fts", "        uses: docker/login-action@0000000000000000000000000000000000000000 # v4", "a step that needs Docker"},
		"a docker make target":                {poolWorkflow, "        run: make coverage-fts", "        run: make docker-build", "a step that needs Docker"},
		"the compose-stack make target":       {poolWorkflow, "        run: make coverage-fts", "        run: make integration-test-standalone INT_PROJECT=\"$P\"", "a step that needs Docker"},
		"docker compose":                      {poolWorkflow, "        run: make coverage-fts", "        run: (cd stack && docker compose config)", "a step that needs Docker"},
		"the docker-compose CLI":              {poolWorkflow, "        run: make coverage-fts", "        run: docker-compose up -d", "a step that needs Docker"},
		"the image loader":                    {poolWorkflow, "        run: make coverage-fts", "        run: scripts/ci-image.sh load \"$DIR\" \"$IMAGE\" \"$ID\"", "a step that needs Docker"},
		"a container":                         {poolWorkflow, "    timeout-minutes: 20\n", "    timeout-minutes: 20\n    container: golang:1.27.3\n", "has a container:"},
		"a service":                           {poolWorkflow, "    timeout-minutes: 20\n", "    timeout-minutes: 20\n    services:\n      db:\n        image: postgres:18\n", "has services:"},
		"pull_request_target":                 {poolWorkflow, "  pull_request:\n", "  pull_request_target:\n", "triggers on pull_request_target"},
		"pull_request_target, listed":         {poolWorkflow, "on:\n  push:\n    branches: [main]\n  pull_request:\n", "on: [push, pull_request_target]\n", "triggers on pull_request_target"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(poolFixture, c.old) {
				t.Fatalf("the fixture lacks %q", c.old)
			}
			body := strings.Replace(poolFixture, c.old, c.new, 1)
			problems, _ := runnerProblems(c.file, []byte(body))
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, c.want) }) {
				t.Fatalf("want a problem containing %q, got %v", c.want, problems)
			}
		})
	}
	for _, expr := range poolRunners {
		for i := range len(expr) {
			c := byte('x')
			if expr[i] == c {
				c = 'y'
			}
			changed := expr[:i] + string(c) + expr[i+1:]
			body := strings.Replace(poolFixture, expr, changed, 1)
			if p, _ := runnerProblems(poolWorkflow, []byte(body)); len(p) == 0 {
				t.Errorf("a runs-on %q, one character off a pool expression, passed", changed)
			}
		}
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
