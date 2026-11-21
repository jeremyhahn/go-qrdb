// ci_runners_test.go is the gate over where the workflows run (DS-7377;
// owner rulings 999 and 1006, which supersede Tenant Agent rulings 833 and
// 834 and their two-pool rule).
//
// GitHub-hosted first: every job of every workflow runs on the standard
// GitHub-hosted ubuntu-latest runner. A job that cannot names one
// self-hosted label and carries a categorized waiver on its runs-on line,
// `# self-hosted: <category>: <reason>` (waiverProblem): `hardware` (it
// needs hardware no hosted runner has) or `oversize` (it exceeds a measured
// hosted limit: disk, memory, the 6-hour job limit), in any workflow; or
// `credential`, for code-review.yml's Codex review job alone, whose
// reviewer token is mounted on that runner only. The review-only labels
// codex and qwen (reviewRunnerLabels) are reserved to their code-review.yml
// review jobs whatever the waiver category (Tenant Agent ruling 1007). The self-hosted machinery
// the forge's runners needed -- the bigdisk pools and their routing
// expressions, the batch and ci-gate labels, the forge registry, the named
// cache volumes, the stale-stack reaper -- is gone. It fails when:
//
//   - a job's runs-on is neither the literal `ubuntu-latest` nor one
//     self-hosted label whose line comment passes waiverProblem;
//   - a self-hosted job has a step that wipes a hosted runner's disk (sudo
//     rm, docker prune) not gated to the hosted runner;
//   - ci.yml's playwrightJob leaves a credential in its checkout or runs an
//     apt install off the hosted runner (Company Agent m2396);
//   - an `if:` still splits on github.server_url, outside the jobs that speak
//     the Gitea API (giteaOnlyJobs) and batch.yml, whose splits are left to
//     their owner;
//   - a removed script is back in scripts/;
//   - a job has no timeout-minutes, or one over jobMinutesCeiling;
//   - an actions/cache entry is saved on any event but a push to main, or by
//     the combined actions/cache action, which saves on every event;
//   - a workflow writes a GOMAXPROCS or go -p count instead of letting
//     scripts/in-toolchain-image.sh derive it from the runner.
//
// code-review.yml is a copy of the code-review template: its runs-on lines
// are held to the waiver rule above, and the template keeps its other rules.
//
// The discovery floors keep an empty parse from passing: every other
// assertion is inside a loop, and an empty loop reports success.
package buildgates

import (
	"encoding/json"
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

const (
	// jobMinutesCeiling is the most minutes any job may be given: a hosted
	// job is killed at 360, and a limit under that names the runaway job
	// rather than letting the runner cancel it unnamed.
	jobMinutesCeiling = 350
	// hostedRunner is the runs-on every job names unless it carries a waiver.
	hostedRunner = "ubuntu-latest"
	// ciFile is the main CI workflow, held to a floor of hosted jobs.
	ciFile = "ci.yml"
	// poolSplit is the condition preflightRunner holds the bigdisk pool to: a
	// push to main or a pull request whose head is this repository. Anything
	// else, a fork's pull request, a schedule or a dispatch, gets ubuntu-latest.
	poolSplit = `((github.event_name == 'push' && github.ref == 'refs/heads/main') || (github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository))`
	// preflightRunner is ci.yml's preflight runs-on since ci-template v1.49
	// (Tenant Agent ruling 1047 (3)): the bigdisk pool behind poolSplit, so a
	// fork's PR, a schedule and a dispatch stay hosted, overridden by the
	// repository variable CI_PREFLIGHT_RUNS_ON. Only the preflight job reads it.
	// It is the one runs-on expression in ci.yml besides selectionRunners, and
	// like them it carries no `# self-hosted:` waiver: it is held to this
	// text, character for character, instead.
	preflightRunner = `${{ ` + poolSplit + ` && fromJSON(vars.CI_PREFLIGHT_RUNS_ON || '["self-hosted","linux","x64","bigdisk"]') || 'ubuntu-latest' }}`
	// reviewWorkflow is the code-review template's copy, and reviewJob its
	// Codex review job, the one job a credential waiver may name.
	reviewWorkflow = "code-review.yml"
	reviewJob      = "review"
	// hostedStepTerm is the conjunct a step carries in its `if:` so that it
	// never runs on a self-hosted runner: a step that wipes a hosted
	// runner's disk.
	hostedStepTerm = "runner.environment == 'github-hosted'"
	// playwrightJob runs its make target natively (Company Agent m2396,
	// 2026-11-10), so it is held to leaving no credential in its checkout
	// and installing no apt package off the hosted runner.
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
	// hostedJobFloor, under ci.yml's count (18 of its 19 jobs hosted by
	// default, on ubuntu-latest or, since v1.48, on a selectionRunners
	// expression; preflight is on the bigdisk pool since v1.49): a
	// regression of the workflow off the hosted runner fails here.
	hostedJobFloor = 15
	// selfHostedWaiver opens the waiver a self-hosted runs-on line carries:
	// `# self-hosted: <category>: <reason>` (owner rulings 999 and 1006).
	selfHostedWaiver = "# self-hosted: "
	// credentialCategory is the waiver category reviewJob alone may name.
	credentialCategory = "credential"
)

// waiverCategories are the categories any job's waiver may name: hardware
// no hosted runner has, or a measured hosted limit the job exceeds.
var waiverCategories = []string{"hardware", "oversize"}

// reviewRunnerLabels maps each review-only runner label to the one
// code-review.yml job that may run on it (Tenant Agent ruling 1007): codex
// carries the Codex reviewer, qwen the opencode reviewer's model.
var reviewRunnerLabels = map[string]string{"codex": reviewJob, "qwen": "review-opencode"}

// selectionRunners are ci-template v1.48's runs-on expressions (Tenant Agent
// ruling 1024 item 1), character for character, each with the pool its
// overflow names. They read the preflight step `Hosted or overflow runner
// (ruling 1024)`, which outputs "ubuntu-latest" unless the hosted pool is
// full, so a job on one is a hosted job by default and needs no
// `# self-hosted:` waiver; it must need preflight, whose output it reads. A
// job on bigdisk (no Docker daemon) needs no Docker.
var selectionRunners = map[string]string{
	"${{ fromJSON(needs.preflight.outputs.runner_docker) }}":               "bigdisk-docker",
	"${{ fromJSON(needs.preflight.outputs.runner) }}":                      "bigdisk",
	`${{ fromJSON(needs.preflight.outputs.runner || '"ubuntu-latest"') }}`: "bigdisk",
}

// needsJob reports whether a job's needs: names want, as a scalar or in a list.
func needsJob(n yaml.Node, want string) bool {
	if n.Kind == yaml.ScalarNode {
		return n.Value == want
	}
	return slices.ContainsFunc(n.Content, func(c *yaml.Node) bool { return c.Value == want })
}

// dockerStep matches a step that needs Docker: the docker or docker-compose
// CLI, a docker/* action, a make target named docker-* or the compose-stack
// integration-test-* and integration-down*, or the image wrappers
// (scripts/in-toolchain-image.sh, scripts/ci-image.sh). A job name such as
// docker-build, which ci-gate reads the result of, is not a Docker use, so
// the CLI matches only as a command word.
var dockerStep = regexp.MustCompile(`(^|[^\w.-])docker(-compose)?(\s|$)|(^|[^\w.-])docker/|\bmake\b[^\n]*[\s'"](docker-|integration-test|integration-down)|in-toolchain-image\.sh|ci-image\.sh`)

// dockerNeeds is every way a job needs Docker on a bigdisk host: a container,
// services, or a step that needs Docker (dockerStep) and is not gated to the
// hosted runner (hostedStepTerm). A job that can overflow to the bigdisk
// pool, which has no Docker daemon, must have none.
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

// releaseLabel is the release pool's runner label, which no job may name.
const releaseLabel = "release"

// hostWipeStep matches a step that frees a hosted runner's disk: a sudo rm
// of the preinstalled SDKs, or a docker prune that would delete the images
// other jobs on a shared self-hosted runner hold.
var hostWipeStep = regexp.MustCompile(`\bsudo\s+rm\b|\bdocker\s+(image\s+|system\s+|container\s+|builder\s+)?prune\b`)

// giteaOnlyJobs speak the Gitea API (its issue queue, its merge endpoint) and
// so do not run on github.com: the server_url guard is what keeps them off.
var giteaOnlyJobs = map[string]map[string]bool{
	"nightly.yml": {"signal": true},
}

// serverURLExemptWorkflows keep their server_url splits for now: batch.yml's
// failure-notification steps are its owner's to collapse.
var serverURLExemptWorkflows = map[string]bool{"batch.yml": true}

// bigdiskFallback is a runs-on that resolves to ubuntu-latest on github.com
// and to the persistent bigdisk pool elsewhere; no job may name it unwaived.
const bigdiskFallback = "${{ github.server_url == 'https://github.com' && 'ubuntu-latest' || 'bigdisk' }}"

// removedScripts are the scripts that implemented the self-hosted CI
// DS-7377 removed.
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
	Needs     yaml.Node `yaml:"needs"`
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
	add := func(format string, a ...string) {
		problems = append(problems, file+": "+strings.Join(append([]string{format}, a...), ""))
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
		case runsOn == hostedRunner:
			// Owner rulings 999 and 1006: the default. v1.48 (ruling 1024): a
			// ci.yml job that waits on preflight reads its runner selection.
			if file == ciFile && needsJob(job.Needs, "preflight") {
				add("job " + name + " needs preflight but runs on the literal " + hostedRunner + "; it reads the runner selection, needs.preflight.outputs.runner or runner_docker (ruling 1024)")
			}
		case file == ciFile && selectionRunners[runsOn] != "":
			// v1.48 (ruling 1024): hosted by default, the Dells on overflow,
			// so no `# self-hosted:` waiver; preflight keeps forks hosted.
			if !needsJob(job.Needs, "preflight") {
				add("job " + name + " runs on the runner selection " + runsOn + " but does not need preflight, whose output it reads (ruling 1024)")
			}
			if selectionRunners[runsOn] == "bigdisk" {
				for _, p := range dockerNeeds(job) {
					add("job " + name + " overflows to the Docker-free bigdisk pool (needs.preflight.outputs.runner), and " + p + "; a job that needs Docker reads runner_docker")
				}
			}
			problems = append(problems, wipeProblems(file, name, job)...)
		case file == ciFile && runsOn == preflightRunner:
			// v1.49 (ruling 1047 (3)): preflight alone, Docker-free, needs
			// nothing; poolSplit keeps a fork's PR hosted.
			if name != "preflight" {
				add("job " + name + " runs on preflight's runs-on, which reads vars.CI_PREFLIGHT_RUNS_ON; only the preflight job may (ci-template v1.49, ruling 1047 (3))")
			}
			for _, p := range dockerNeeds(job) {
				add("job " + name + " runs on the Docker-free bigdisk pool (preflightRunner), and " + p)
			}
			problems = append(problems, wipeProblems(file, name, job)...)
		case runsOn == "" || strings.Contains(runsOn, "${{"):
			add("job "+name+" runs on ", nodeText(job.RunsOn), ", not the literal "+hostedRunner+" nor one self-hosted label (owner ruling 999)")
		default:
			if p := reservedLabelProblem(file, name, runsOn); p != "" {
				add("job "+name+" runs on ", nodeText(job.RunsOn), ", and "+p)
			}
			if p := waiverProblem(file, name, job.RunsOn.LineComment); p != "" {
				add("job "+name+" runs on ", nodeText(job.RunsOn), ", not the literal "+hostedRunner+", and "+p)
			}
			if p := forkProblem(onTriggers(wf.On), job.If); p != "" {
				add("job "+name+" runs on ", nodeText(job.RunsOn), ", a self-hosted runner, and "+p)
			}
			problems = append(problems, wipeProblems(file, name, job)...)
		}
		if file == reviewWorkflow {
			// The code-review template keeps its own rules.
			continue
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

// wipeProblems is each step of a job that can run on a self-hosted runner
// and wipes a hosted runner's disk without hostedStepTerm in its if:.
func wipeProblems(file, name string, job runnerJob) (problems []string) {
	for _, s := range job.Steps {
		if hostWipeStep.MatchString(s.Run) && !strings.Contains(s.If, hostedStepTerm) {
			problems = append(problems, file+": job "+name+" has a step that wipes a hosted runner's disk under if: "+strconv.Quote(s.If)+", without "+hostedStepTerm+"; on a self-hosted runner it deletes what other jobs share")
		}
	}
	return problems
}

// waiverProblem returns "" when comment, the line comment of job's runs-on
// in workflow, holds the waiver `# self-hosted: <category>: <reason>` with
// a category in waiverCategories, or credentialCategory on reviewJob in
// reviewWorkflow, and a non-empty reason; otherwise what is wrong with it.
// reservedLabelProblem says why a job may not run on label, or "" when it
// may: a label in reviewRunnerLabels serves only its one code-review.yml
// job, whatever the waiver category (Tenant Agent ruling 1007).
func reservedLabelProblem(workflow, job, label string) string {
	if strings.EqualFold(label, releaseLabel) {
		// This gate reads no if:, so it cannot tell a release job's guard from
		// one a pull_request satisfies (github.event.ref is '' there, #1642).
		return "the " + releaseLabel + " label is the release pool's, which no job here runs on; a waiver does not open it"
	}
	// GitHub matches runner labels case-insensitively, so QWEN is qwen (#1642).
	owner, ok := reviewRunnerLabels[strings.ToLower(label)]
	if !ok || (workflow == reviewWorkflow && job == owner) {
		return ""
	}
	return "the " + label + " label is reserved to " + reviewWorkflow + "'s " + owner + " job whatever the waiver (Tenant Agent ruling 1007)"
}

func waiverProblem(workflow, job, comment string) string {
	_, waiver, ok := strings.Cut(comment, selfHostedWaiver)
	if !ok {
		return "its runs-on line carries no `" + selfHostedWaiver + "<category>: <reason>` waiver (owner ruling 999)"
	}
	category, reason, ok := strings.Cut(waiver, ":")
	category = strings.TrimSpace(category)
	if !ok || category == "" || strings.ContainsAny(category, " \t") {
		return "its waiver names no category; it reads `" + selfHostedWaiver + "<category>: <reason>`, the category one of " + strings.Join(append(slices.Clone(waiverCategories), credentialCategory), ", ")
	}
	switch {
	case slices.Contains(waiverCategories, category):
	case category == credentialCategory:
		if workflow != reviewWorkflow || job != reviewJob {
			return "its waiver names the credential category, which only " + reviewWorkflow + "'s " + reviewJob + " job, the Codex review, may name"
		}
	default:
		return "its waiver names the unknown category " + strconv.Quote(category) + "; the categories are " + strings.Join(append(slices.Clone(waiverCategories), credentialCategory), ", ")
	}
	if strings.TrimSpace(reason) == "" {
		return "its " + category + " waiver gives no reason"
	}
	return ""
}

// forkPREvents are the triggers whose run a fork's pull request starts.
var forkPREvents = []string{"pull_request", "pull_request_target"}

// forkSafeEvent matches a top-level `github.event_name == '<event>'` conjunct.
var forkSafeEvent = regexp.MustCompile(`^github\.event_name == '([a-z_]+)'$|^'([a-z_]+)' == github\.event_name$`)

// forkProblem: a waiver does not make fork code trusted (go-qrdb #1642's
// Codex review). In a workflow a pull_request or pull_request_target event
// triggers, a self-hosted job's if: has a top-level conjunct that keeps a
// fork's pull request off the runner: the head in this repository, an event
// that is not a pull request's, or a forge that is not github.com.
func forkProblem(triggers []string, cond string) string {
	var events []string
	for _, ev := range forkPREvents {
		if slices.Contains(triggers, ev) {
			events = append(events, ev)
		}
	}
	if len(events) == 0 {
		return ""
	}
	for _, c := range topConjuncts(cond) {
		if forkSafeConjunct(c) {
			return ""
		}
	}
	return "its workflow runs on " + strings.Join(events, " and ") + ", but its if: has no top-level `&& " + forkGuardTerm +
		"`: a fork's pull request would run its code on a persistent host, and a hardware or oversize waiver does not make fork code trusted"
}

// forkGuardTerm is the conjunct that keeps a fork's head off a self-hosted runner.
const forkGuardTerm = "github.event.pull_request.head.repo.full_name == github.repository"

// forkSafeConjunct reports whether one top-level conjunct of an if: is false
// for every fork pull request.
func forkSafeConjunct(c string) bool {
	switch c {
	case forkGuardTerm, "github.repository == github.event.pull_request.head.repo.full_name",
		"github.server_url != 'https://github.com'", "'https://github.com' != github.server_url":
		return true
	}
	m := forkSafeEvent.FindStringSubmatch(c)
	if m == nil {
		return false
	}
	return !slices.Contains(forkPREvents, m[1]+m[2])
}

// topConjuncts splits an if: at its top-level && operators, outside
// parentheses and string literals, each part lower-cased with its
// whitespace collapsed. An if: with a top-level || has no conjunct every
// run satisfies: it returns none.
func topConjuncts(cond string) []string {
	s := strings.Join(strings.Fields(strings.ToLower(cond)), " ")
	if inner, ok := strings.CutPrefix(s, "${{"); ok {
		if inner, ok = strings.CutSuffix(inner, "}}"); ok {
			s = strings.TrimSpace(inner)
		}
	}
	if s == "" {
		return nil
	}
	var parts []string
	depth, quoted, start := 0, false, 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\'':
			quoted = !quoted
		case quoted:
		case s[i] == '(':
			depth++
		case s[i] == ')':
			depth--
		case depth == 0 && strings.HasPrefix(s[i:], "||"):
			return nil
		case depth == 0 && strings.HasPrefix(s[i:], "&&"):
			parts = append(parts, strings.TrimSpace(s[start:i]))
			start = i + 2
			i++
		}
	}
	return append(parts, strings.TrimSpace(s[start:]))
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
// running on ubuntu-latest (owner rulings 999 and 1006): a floor of hosted
// jobs, so that a regression of the workflow off the hosted runner fails here
// too.

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
	if err := yaml.Unmarshal(files[ciFile], &wf); err != nil {
		t.Fatalf("parse %s: %v", ciFile, err)
	}
	hosted := 0
	for _, job := range wf.Jobs {
		if job.RunsOn.Value == hostedRunner || selectionRunners[job.RunsOn.Value] != "" {
			hosted++
		}
	}
	if hosted < hostedJobFloor {
		t.Errorf("%s has %d jobs on %s, under the floor of %d (owner rulings 999 and 1006)", ciFile, hosted, hostedRunner, hostedJobFloor)
	}
}

// TestCIJobsReadPreflightSelection: v1.48 (ruling 1024) on ci.yml. Every job
// but preflight, which computes the selection, and signatures, which waits on
// nothing, runs on a selectionRunners expression and needs preflight; a job
// whose steps need Docker reads runner_docker (Codex review 6000705149).
// Preflight runs on preflightRunner (v1.49, ruling 1047 (3)), signatures on
// the literal ubuntu-latest.
func TestCIJobsReadPreflightSelection(t *testing.T) {
	files := workflowFiles(t)
	var wf runnerWorkflow
	if err := yaml.Unmarshal(files[ciFile], &wf); err != nil {
		t.Fatalf("parse %s: %v", ciFile, err)
	}
	// signatures and deps-bump-dispatch (ruling 640: the nightly's 5-minute
	// dispatch of deps-bump.yml, outside ci-gate) need no job, so they stay
	// on the literal hosted runner instead of waiting on preflight.
	want := map[string]string{"preflight": preflightRunner, "signatures": hostedRunner, "deps-bump-dispatch": hostedRunner}
	for name, job := range wf.Jobs {
		runsOn := job.RunsOn.Value
		if w, ok := want[name]; ok {
			if runsOn != w {
				t.Errorf("job %s runs on %s, not %s", name, nodeText(job.RunsOn), w)
			}
			continue
		}
		pool := selectionRunners[runsOn]
		if pool == "" {
			t.Errorf("job %s runs on %s, not the preflight runner selection (ruling 1024)", name, nodeText(job.RunsOn))
			continue
		}
		if !needsJob(job.Needs, "preflight") {
			t.Errorf("job %s reads the runner selection but does not need preflight", name)
		}
		if len(dockerNeeds(job)) > 0 && pool != "bigdisk-docker" {
			t.Errorf("job %s needs Docker but runs on %s, not needs.preflight.outputs.runner_docker", name, runsOn)
		}
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
	// The Runner labels step (ci-template v1.47, verbatim) evaluates other
	// workflows' runs-on expressions and so names github.server_url in its
	// python; that is the lint's text, not a dependency of ci.yml on the host.
	body := readCIBody(t)
	if i := strings.Index(body, runnerLabelsStep); i >= 0 {
		if end := strings.Index(body[i:], "\n          PY\n"); end >= 0 {
			body = body[:i] + body[i+end:]
		}
	}
	if strings.Contains(body, "server_url") {
		t.Error("ci.yml names github.server_url; it runs on github.com alone, so nothing in it depends on the host")
	}
}

// runnerLabelsStep is ci-template v1.47's step that checks every workflow's
// runs-on label and waiver category together (Tenant Agent ruling 1007).
const runnerLabelsStep = "- name: Runner labels (ruling 633)\n"

// runnerLabelsTestsStep runs the Python gate's own accepting and rejecting
// cases before the gate, so CI goes red when the gate stops refusing
// (go-qrdb #1642's Codex review).
const runnerLabelsTestsStep = "- name: Runner labels tests (go-qrdb #1642)\n        run: python3 -I .github/workflows/testdata/runner-labels-tests.py\n"

func TestCIRunnerLabelsStepPresent(t *testing.T) {
	body := readCIBody(t)
	if n := strings.Count(body, runnerLabelsStep); n != 1 {
		t.Errorf("ci.yml has %d %q steps, want exactly one (ci-template v1.47, ruling 1007)", n, strings.TrimSpace(runnerLabelsStep))
	}
	tests := strings.Index(body, runnerLabelsTestsStep)
	switch {
	case tests < 0 || strings.Count(body, runnerLabelsTestsStep) != 1:
		t.Errorf("ci.yml needs exactly one step %q that runs testdata/runner-labels-tests.py", runnerLabelsTestsStep)
	case tests > strings.Index(body, runnerLabelsStep):
		t.Error("ci.yml runs the Runner labels tests after the gate; they must run before it")
	}
	const review = `REVIEW = {"codex": "credential", "qwen": "hardware"}`
	if !strings.Contains(body, review) {
		t.Errorf("ci.yml's Runner labels step lacks %q: codex and qwen must be reserved to code-review.yml with their own categories (ruling 1007)", review)
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
		"a self-hosted label":                                {hostedFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: bigdisk", "not the literal ubuntu-latest"},
		"a runner list":                                      {hostedFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: [self-hosted, linux]", "not the literal ubuntu-latest"},
		"an expression":                                      {hostedFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: ${{ github.server_url == 'https://github.com' && 'ubuntu-latest' || 'batch' }}", "not the literal ubuntu-latest"},
		"a job server_url split":                             {hostedFile, "    timeout-minutes: 30\n", "    timeout-minutes: 30\n    if: github.server_url != 'https://github.com'\n", "splits on github.server_url"},
		"a step server_url split":                            {"nightly.yml", "      - run: make build", "      - if: github.server_url == 'https://github.com'\n        run: make build", "splits on github.server_url"},
		"no timeout":                                         {hostedFile, "    timeout-minutes: 30\n", "", "has no timeout-minutes"},
		"a timeout over the ceiling":                         {hostedFile, "timeout-minutes: 30", "timeout-minutes: 351", "not a number of minutes from 1 to 350"},
		"a matrix leg over the ceiling":                      {hostedFile, "minutes: 350", "minutes: 360", "not a number of minutes from 1 to 350"},
		"a matrix limit nothing sets":                        {hostedFile, "timeout-minutes: ${{ matrix.minutes }}", "timeout-minutes: ${{ matrix.limit }}", "which no matrix entry sets"},
		"a cache saved on every event":                       {hostedFile, "${{ !cancelled() && env.CI_PUBLISH == 'true' }}", "${{ !cancelled() }}", "only a push to main saves one"},
		"the combined cache action":                          {hostedFile, "actions/cache/restore@", "actions/cache@", "combined actions/cache"},
		"a publish flag on every push":                       {hostedFile, "github.event_name == 'push' && github.ref == 'refs/heads/main'", "github.event_name == 'push'", "env.CI_PUBLISH is"},
		"a written GOMAXPROCS":                               {hostedFile, "make leg", "GOMAXPROCS=4 make leg", "GOMAXPROCS or -p count"},
		"a written -p":                                       {hostedFile, "make leg", "go test -p 4 ./...", "GOMAXPROCS or -p count"},
		"a GOMAXPROCS step env":                              {hostedFile, "      - run: make leg", "      - run: make leg\n        env:\n          GOMAXPROCS: \"8\"", "sets GOMAXPROCS in a step's env"},
		"a ci.yml job on ubuntu-latest that needs preflight": {ciFile, "  build:\n    runs-on: ubuntu-latest\n", "  build:\n    runs-on: ubuntu-latest\n    needs: preflight\n", "needs preflight but runs on the literal ubuntu-latest"},
		"a self-hosted label in ci.yml":                      {ciFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: bigdisk", "carries no `# self-hosted: <category>: <reason>` waiver"},
		"an expression with a waiver":                        {ciFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: ${{ 'bigdisk' }}  # self-hosted: oversize: a fixture reason", "not the literal ubuntu-latest nor one self-hosted label"},
		"a review label in ci.yml":                           {ciFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: codex  # self-hosted: hardware: a fixture reason", "label is reserved to code-review.yml's review job"},
		"a disk wipe on a waived job":                        {hostedFile, "  build:\n    runs-on: ubuntu-latest\n    timeout-minutes: 30\n    steps:\n", "  build:\n    runs-on: gpu  # self-hosted: hardware: a fixture reason\n    timeout-minutes: 30\n    steps:\n      - run: sudo rm -rf /usr/share/dotnet\n", "wipes a hosted runner's disk"},
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

// TestRunnerProblems_Exemptions holds the exemptions to their scope: a
// waived self-hosted label (the credential category on code-review.yml's
// review job alone), a Gitea-API job's server_url guard, and batch.yml's
// splits.
func TestRunnerProblems_Exemptions(t *testing.T) {
	label := func(job, runsOn string) string {
		return strings.Replace(runnerFixture, "  build:\n    runs-on: ubuntu-latest", "  "+job+":\n    runs-on: "+runsOn, 1)
	}
	const credential = "codex  # lab-string-ok: a fixture note # self-hosted: credential: a fixture reason"
	if p, _ := runnerProblems(reviewWorkflow, []byte(label(reviewJob, credential))); len(p) != 0 {
		t.Errorf("code-review.yml's review job keeps its credential waiver, got %v", p)
	}
	for _, c := range []struct{ file, job, runsOn string }{
		{reviewWorkflow, reviewJob, "codex"},
		{reviewWorkflow, "build", credential},
		{ciFile, reviewJob, credential},
		// An unwaived bigdisk fallback is the persistent pool on Gitea,
		// whichever job or workflow names it (owner ruling 999, #1642).
		{reviewWorkflow, reviewJob, bigdiskFallback},
		{reviewWorkflow, "build", bigdiskFallback},
		{reviewWorkflow, "scope", bigdiskFallback},
		{reviewWorkflow, "gate", bigdiskFallback},
		{ciFile, "gate", bigdiskFallback},
		{reviewWorkflow, "gate", "bigdisk"},
		{reviewWorkflow, "gate", "${{ 'ubuntu-latest' }}"},
	} {
		if p, _ := runnerProblems(c.file, []byte(label(c.job, c.runsOn))); len(p) == 0 {
			t.Errorf("%s job %s on %q passed", c.file, c.job, c.runsOn)
		}
	}
	if p, _ := runnerProblems(ciFile, []byte(label("build", "gpu  # self-hosted: hardware: a fixture reason"))); len(p) != 0 {
		t.Errorf("a hardware waiver holds in any workflow, got %v", p)
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

// TestWaiverProblem holds the waiver to its format and categories (owner
// rulings 999 and 1006).
func TestWaiverProblem(t *testing.T) {
	cases := []struct {
		name, workflow, job, comment, want string
	}{
		{"hardware", ciFile, "build", "# self-hosted: hardware: the GPU host", ""},
		{"oversize", ciFile, "build", "# self-hosted: oversize: 40 GB of disk, measured", ""},
		{"after another note", reviewWorkflow, "review-opencode", "# CUSTOMIZE: a note # self-hosted: hardware: the GPU host", ""},
		{"credential on the Codex review job", reviewWorkflow, reviewJob, "# lab-string-ok: a note # self-hosted: credential: the reviewer token", ""},
		{"credential on another job", reviewWorkflow, "review-opencode", "# self-hosted: credential: the reviewer token", "only code-review.yml's review job"},
		{"credential in another workflow", ciFile, reviewJob, "# self-hosted: credential: the reviewer token", "only code-review.yml's review job"},
		{"no category", ciFile, "build", "# self-hosted: the GPU host", "names no category"},
		{"empty category", ciFile, "build", "# self-hosted: : the GPU host", "names no category"},
		{"unknown category", ciFile, "build", "# self-hosted: speed: it is faster", "unknown category \"speed\""},
		{"empty reason", ciFile, "build", "# self-hosted: hardware:", "gives no reason"},
		{"blank reason", ciFile, "build", "# self-hosted: oversize:   ", "gives no reason"},
		{"no waiver", ciFile, "build", "", "carries no"},
		{"another comment only", reviewWorkflow, reviewJob, "# lab-string-ok: a note", "carries no"},
		{"the bare marker", ciFile, "build", "# self-hosted:", "carries no"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := waiverProblem(c.workflow, c.job, c.comment)
			switch {
			case c.want == "" && got != "":
				t.Fatalf("waiverProblem(%q, %q, %q) = %q, want none", c.workflow, c.job, c.comment, got)
			case c.want != "" && !strings.Contains(got, c.want):
				t.Fatalf("waiverProblem(%q, %q, %q) = %q, want one containing %q", c.workflow, c.job, c.comment, got, c.want)
			}
		})
	}
}

// TestReviewLabelsReserved holds the review-only runner labels to their
// code-review.yml jobs whatever the waiver category (Tenant Agent ruling
// 1007): a hardware or oversize waiver does not open codex or qwen to
// another job or workflow.
func TestReviewLabelsReserved(t *testing.T) {
	cases := []struct {
		name, file, job, runsOn string
		fail                    bool
	}{
		{"ci.yml on codex", ciFile, "build", "codex  # self-hosted: hardware: a fixture reason", true},
		{"ci.yml on qwen", ciFile, "build", "qwen  # self-hosted: hardware: a fixture reason", true},
		{"nightly.yml on qwen, oversize", "nightly.yml", "build", "qwen  # self-hosted: oversize: a fixture reason", true},
		{"another review job on codex", reviewWorkflow, "summary", "codex  # self-hosted: hardware: a fixture reason", true},
		{"review-opencode on codex", reviewWorkflow, "review-opencode", "codex  # self-hosted: hardware: a fixture reason", true},
		{"the Codex review on codex", reviewWorkflow, reviewJob, "codex  # self-hosted: credential: the reviewer token", false},
		{"review-opencode on qwen", reviewWorkflow, "review-opencode", "qwen  # self-hosted: hardware: a fixture reason", false},
		{"ci.yml on another label", ciFile, "build", "gpu  # self-hosted: hardware: a fixture reason", false},
		// GitHub matches runner labels case-insensitively (#1642).
		{"a non-review job on QWEN", reviewWorkflow, "build", "QWEN  # self-hosted: hardware: a fixture reason", true},
		{"ci.yml on Codex", ciFile, "build", "Codex  # self-hosted: hardware: a fixture reason", true},
		{"review on QWEN", reviewWorkflow, reviewJob, "QWEN  # self-hosted: hardware: a fixture reason", true},
		{"review-opencode on QWEN", reviewWorkflow, "review-opencode", "QWEN  # self-hosted: hardware: a fixture reason", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := "on: push\njobs:\n  " + c.job + ":\n    runs-on: " + c.runsOn + "\n    timeout-minutes: 30\n    steps:\n      - run: make build\n"
			problems, jobs := runnerProblems(c.file, []byte(body))
			if jobs != 1 {
				t.Fatalf("the fixture parsed to %d jobs, want 1: %v", jobs, problems)
			}
			reserved := slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, "reserved to") })
			runsOn := slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, "job "+c.job+" runs on ") })
			switch {
			case c.fail && !reserved:
				t.Fatalf("want a problem naming the label reserved to its review job, got %v", problems)
			case !c.fail && runsOn:
				t.Fatalf("want no runs-on problem, got %v", problems)
			}
		})
	}
}

// TestReleaseLabelRefused holds the release label shut whatever the waiver:
// this gate reads no if:, so a waived release job guarded by
// `github.event.ref == ”` would run on a pull_request, whose payload has no
// top-level ref (#1642).
func TestReleaseLabelRefused(t *testing.T) {
	cases := []struct{ name, file, on, runsOn string }{
		{"ci.yml, hardware waiver, empty event.ref guard", ciFile, "pull_request", "release  # self-hosted: hardware: a fixture reason"},
		{"release.yml, oversize waiver, pull_request_target", hostedFile, "pull_request_target", "release  # self-hosted: oversize: a fixture reason"},
		{"upper case on push", "nightly.yml", "push", "RELEASE  # self-hosted: hardware: a fixture reason"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := "on: " + c.on + "\njobs:\n  pub:\n    if: ${{ github.event.ref == '' }}\n    runs-on: " + c.runsOn + "\n    timeout-minutes: 30\n    steps:\n      - run: make build\n"
			problems, jobs := runnerProblems(c.file, []byte(body))
			if jobs != 1 {
				t.Fatalf("the fixture parsed to %d jobs, want 1: %v", jobs, problems)
			}
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, "the "+releaseLabel+" label") }) {
				t.Fatalf("want a problem refusing the %s label, got %v", releaseLabel, problems)
			}
		})
	}
}

// TestForkIsolation: a hardware or oversize waiver does not make fork code
// trusted (go-qrdb #1642's Codex review). A waived self-hosted job in a
// workflow a pull_request or pull_request_target event triggers keeps a
// fork's pull request off its runner: a top-level conjunct of its if:
// requires a head in this repository, a non-PR event, or another forge.
func TestForkIsolation(t *testing.T) {
	const steps = "    timeout-minutes: 30\n    steps:\n      - run: gh pr checkout \"$PR\" && make test\n"
	fixture := func(on, cond string) string {
		body := "on: " + on + "\njobs:\n  gpu:\n"
		if cond != "" {
			body += "    if: " + cond + "\n"
		}
		return body + "    runs-on: gpu  # self-hosted: hardware: the GPU bench\n" + steps
	}
	refused := []struct{ name, body string }{
		{"pull_request_target, no if, checks out the PR head", fixture("pull_request_target", "")},
		{"pull_request, no if", fixture("pull_request", "")},
		{"pull_request and push, no if", fixture("[pull_request, push]", "")},
		{"pull_request_target, a label guard alone", fixture("pull_request_target", "contains(github.event.pull_request.labels.*.name, 'gpu')")},
		{"pull_request, the head guard under a top-level ||", fixture("pull_request", "github.event.pull_request.head.repo.full_name == github.repository || github.event_name == 'pull_request'")},
		{"pull_request, the head guard negated", fixture("pull_request", "github.event.pull_request.head.repo.full_name != github.repository")},
		{"pull_request, an event guard naming pull_request_target", fixture("pull_request", "github.event_name == 'pull_request_target'")},
		{"pull_request, the head guard inside a parenthesised ||", fixture("pull_request", "${{ (github.event.pull_request.head.repo.full_name == github.repository || true) && true }}")},
	}
	for _, c := range refused {
		t.Run("refused/"+c.name, func(t *testing.T) {
			problems, jobs := runnerProblems("gpu.yml", []byte(c.body))
			if jobs != 1 {
				t.Fatalf("the fixture parsed to %d jobs, want 1: %v", jobs, problems)
			}
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, "fork") }) {
				t.Fatalf("want a fork-isolation problem, got %v", problems)
			}
		})
	}
	passed := []struct{ name, file, body string }{
		{"push only, no if", "gpu.yml", fixture("push", "")},
		{"pull_request, the head guard", "gpu.yml", fixture("pull_request", "github.event.pull_request.head.repo.full_name == github.repository")},
		{"pull_request_target, the head guard reversed, folded with another term", "gpu.yml", fixture("pull_request_target", "${{ github.event.pull_request.state == 'open'\n      && github.repository == github.event.pull_request.head.repo.full_name }}")},
		{"pull_request and push, a push guard", "gpu.yml", fixture("[pull_request, push]", "github.event_name == 'push' && github.ref == 'refs/heads/main'")},
		// code-review.yml's review-opencode job: Gitea only.
		{"pull_request, another forge only", reviewWorkflow, fixture("pull_request", "needs.x.outputs.y == 'z' && github.server_url != 'https://github.com'")},
	}
	for _, c := range passed {
		t.Run("passed/"+c.name, func(t *testing.T) {
			problems, jobs := runnerProblems(c.file, []byte(c.body))
			if jobs != 1 || len(problems) != 0 {
				t.Fatalf("want 1 job and no problem, got %d jobs: %v", jobs, problems)
			}
		})
	}
}

// selectionFixture is a ci.yml on ci-template v1.49: preflight on
// preflightRunner (ruling 1047 (3)), and v1.48's runner selection (ruling
// 1024), an image job on runner_docker and CI Gate on the guarded runner,
// each needing preflight.
const selectionFixture = `on:
  push:
    branches: [main]
  pull_request:
jobs:
  preflight:
    runs-on: ` + preflightRunner + `   # v1.49
    timeout-minutes: 15
    steps:
      - run: true
  lint:
    runs-on: ${{ fromJSON(needs.preflight.outputs.runner_docker) }}   # v1.48
    needs: preflight
    timeout-minutes: 30
    steps:
      - run: scripts/in-toolchain-image.sh "$IMAGE" "make lint"
  ci-gate:
    runs-on: ${{ fromJSON(needs.preflight.outputs.runner || '"ubuntu-latest"') }}   # v1.48
    needs: [preflight, lint]
    timeout-minutes: 5
    steps:
      - run: true
`

func TestRunnerProblems_Selection(t *testing.T) {
	if p, jobs := runnerProblems(ciFile, []byte(selectionFixture)); len(p) != 0 || jobs != 3 {
		t.Fatalf("the v1.48 runner selection must pass with 3 jobs, got %d jobs: %v", jobs, p)
	}
	cases := map[string]struct {
		file, old, new, want string
	}{
		"a selection job that does not need preflight": {ciFile, "    needs: preflight\n", "", "does not need preflight"},
		"the selection outside ci.yml":                 {hostedFile, "", "", "job lint runs on"},
		"the output without fromJSON":                  {ciFile, "fromJSON(needs.preflight.outputs.runner_docker)", "needs.preflight.outputs.runner_docker", "job lint runs on"},
		"a Docker job overflowing to bigdisk":          {ciFile, "outputs.runner_docker)", "outputs.runner)", "overflows to the Docker-free bigdisk pool"},
		"preflight without the fork split":             {ciFile, " && github.event.pull_request.head.repo.full_name == github.repository", "", "job preflight runs on"},
		"preflight on any push":                        {ciFile, "github.event_name == 'push' && github.ref == 'refs/heads/main'", "github.event_name == 'push'", "job preflight runs on"},
		"the variable read by another job":             {ciFile, "    runs-on: ${{ fromJSON(needs.preflight.outputs.runner_docker) }}   # v1.48", "    runs-on: " + preflightRunner, "only the preflight job may"},
		"a Docker step on preflight":                   {ciFile, "      - run: true\n  lint:", "      - run: docker load -i toolchain.tar\n  lint:", "Docker-free bigdisk pool (preflightRunner)"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(selectionFixture, c.old) {
				t.Fatalf("the fixture lacks %q", c.old)
			}
			body := strings.Replace(selectionFixture, c.old, c.new, 1)
			problems, _ := runnerProblems(c.file, []byte(body))
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, c.want) }) {
				t.Fatalf("want a problem containing %q, got %v", c.want, problems)
			}
		})
	}
	// v1.49: every one-character change to preflight's runs-on is a problem.
	for i := range len(preflightRunner) {
		c := byte('x')
		if preflightRunner[i] == c {
			c = 'y'
		}
		changed := preflightRunner[:i] + string(c) + preflightRunner[i+1:]
		body := strings.Replace(selectionFixture, preflightRunner, changed, 1)
		if p, _ := runnerProblems(ciFile, []byte(body)); len(p) == 0 {
			t.Errorf("a preflight runs-on %q, one character off preflightRunner, passed", changed)
		}
	}
}

// selectionStep returns the run: script and the env: of the ci.yml step with
// id runner, the hosted-or-overflow runner selection (ruling 1024).
func selectionStep(t *testing.T) (script string, env map[string]string) {
	t.Helper()
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				ID  string            `yaml:"id"`
				Env map[string]string `yaml:"env"`
				Run string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(readCIBody(t)), &wf); err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}
	for name, job := range wf.Jobs {
		for _, s := range job.Steps {
			if s.ID != "runner" {
				continue
			}
			if script != "" {
				t.Fatalf("ci.yml has a second step with id runner (job %s)", name)
			}
			script, env = s.Run, s.Env
		}
	}
	if script == "" {
		t.Fatal("ci.yml has no step with id runner, the runner selection")
	}
	return script, env
}

// selectionReply is one recorded Actions API answer: an HTTP code and a body.
type selectionReply struct {
	code int
	body any
}

// stubCurl is the curl the selection step finds first on PATH: it answers
// each URL from <dir>/<key>.json (code in <key>.code, else 200), any other
// URL with 404, and logs every URL in <dir>/calls.log. No request leaves.
const stubCurl = `#!/usr/bin/env bash
dir=%q
out= url=
while [ $# -gt 0 ]; do
  case "$1" in
    -o|-w|-H|--max-time) [ "$1" = -o ] && out=$2; shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
echo "$url" >> "$dir/calls.log"
key=${url//[^A-Za-z0-9]/_}
if [ -f "$dir/$key.json" ]; then
  cp "$dir/$key.json" "$out"
  code=200
  [ -f "$dir/$key.code" ] && code=$(< "$dir/$key.code")
else
  echo '{}' > "$out"
  code=404
fi
printf '%s' "$code"
`

// TestCIRunnerSelectionBehaviour runs the selection step's script, as
// extracted from ci.yml, against a stub curl serving recorded Actions API
// replies, and checks the runner outputs and the annotation it prints.
// The clock needs no fixing: queued jobs are recorded far in the past
// (queued past the grace) or far in the future (not yet past it).
//
// There is no "not github.com" case: ci.yml runs on github.com alone and
// leaves the template's forge check out (its CUSTOMIZE block), so the step
// has no such branch to test.
func TestCIRunnerSelectionBehaviour(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed; the runner selection step is a bash script")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed; the runner selection step needs it")
	}
	script, stepEnv := selectionStep(t)

	const (
		api    = "stub://actions"
		repo   = "owner/repo"
		hosted = `"ubuntu-latest"`
		bigdsk = `["self-hosted","linux","x64","bigdisk"]`
		bigdkr = `["self-hosted","linux","x64","bigdisk-docker"]`
		past   = "2000-01-01T00:00:00Z"
		future = "2999-01-01T00:00:00Z"
	)
	page := func(path string, n int) string { return api + path + "&per_page=100&page=" + strconv.Itoa(n) }
	runList := func(ids ...int) selectionReply {
		list := []map[string]any{}
		for _, id := range ids {
			list = append(list, map[string]any{"id": id})
		}
		return selectionReply{200, map[string]any{"total_count": len(list), "workflow_runs": list}}
	}
	job := func(run int, status, created string) map[string]any {
		return map[string]any{"run_id": run, "status": status, "created_at": created, "labels": []string{"ubuntu-latest"}}
	}
	jobList := func(jobs ...map[string]any) selectionReply {
		return selectionReply{200, map[string]any{"total_count": len(jobs), "jobs": jobs}}
	}
	// world: run 1 (this run) in progress with the given jobs, run 2 queued
	// with the given jobs (no queued run when there are none).
	world := func(own []map[string]any, other []map[string]any) map[string]selectionReply {
		w := map[string]selectionReply{
			page("/runs?status=in_progress", 1):   runList(1),
			page("/runs/1/jobs?filter=latest", 1): jobList(own...),
			page("/runs?status=queued", 1):        runList(),
		}
		if other != nil {
			w[page("/runs?status=queued", 1)] = runList(2)
			w[page("/runs/2/jobs?filter=latest", 1)] = jobList(other...)
		}
		return w
	}
	running := func(n int) []map[string]any {
		var jobs []map[string]any
		for range n {
			jobs = append(jobs, job(1, "in_progress", past))
		}
		return jobs
	}
	short := world(running(1), nil)
	short[page("/runs?status=in_progress", 1)] = selectionReply{200, map[string]any{"total_count": 3, "workflow_runs": []map[string]any{{"id": 1}}}}
	short[page("/runs?status=in_progress", 2)] = selectionReply{200, map[string]any{"total_count": 3, "workflow_runs": []map[string]any{}}}
	failed := world(running(1), nil)
	failed[page("/runs?status=queued", 1)] = selectionReply{500, map[string]any{"message": "server error"}}

	cases := []struct {
		name            string
		limit, headRepo string
		replies         map[string]selectionReply
		runner, docker  string
		notice, warning string // a substring of the one annotation; "" = none
		say             string // a substring of the plain output
		noCurl          bool
	}{
		{name: "below the threshold stays hosted", limit: "2", replies: world(running(1), nil),
			runner: hosted, docker: hosted, say: "1 hosted job(s) in progress"},
		{name: "running at the threshold overflows", limit: "2", headRepo: repo, replies: world(running(2), nil),
			runner: bigdsk, docker: bigdkr, notice: "2 hosted jobs in progress, HOSTED_JOBS_PER_REPO=2"},
		{name: "another run queued past the grace overflows", limit: "2", replies: world(running(1), []map[string]any{job(2, "queued", past)}),
			runner: bigdsk, docker: bigdkr, notice: "1 hosted job(s) queued 60s or more"},
		{name: "another run queued within the grace stays hosted", limit: "2", replies: world(running(1), []map[string]any{job(2, "queued", future)}),
			runner: hosted, docker: hosted, say: "0 hosted job(s) of other runs queued"},
		{name: "a non-200 reply stays hosted with a warning", limit: "2", replies: failed,
			runner: hosted, docker: hosted, warning: "no complete run list (" + api + "/runs?status=queued: 500)"},
		{name: "a listing short of total_count stays hosted with a warning", limit: "2", replies: short,
			runner: hosted, docker: hosted, warning: "1 of 3 entries"},
		{name: "a non-number threshold stays hosted with a warning", limit: "two", replies: world(running(2), nil),
			runner: hosted, docker: hosted, warning: "HOSTED_JOBS_PER_REPO='two'", noCurl: true},
		{name: "a fork pull request at saturation stays hosted", limit: "2", headRepo: "fork/repo", replies: world(running(2), nil),
			runner: hosted, docker: hosted, say: "fork/repo", noCurl: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			stub, bin := filepath.Join(dir, "stub"), filepath.Join(dir, "bin")
			for _, d := range []string{stub, bin} {
				if err := os.Mkdir(d, 0o750); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(strings.Replace(stubCurl, "%q", strconv.Quote(stub), 1)), 0o700); err != nil { // #nosec G306 -- the curl stub must be executable; PATH resolves it for the step script
				t.Fatal(err)
			}
			unsafe := regexp.MustCompile(`[^A-Za-z0-9]`)
			for url, r := range c.replies {
				key := filepath.Join(stub, unsafe.ReplaceAllString(url, "_"))
				body, err := json.Marshal(r.body)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(key+".json", body, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(key+".code", []byte(strconv.Itoa(r.code)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			env := map[string]string{
				"API": api, "RUN_ID": "1", "JOB_TOKEN": "stub-token", "LIMIT": c.limit, "GRACE": "60",
				"HEAD_REPO": c.headRepo, "REPO": repo,
			}
			for k := range stepEnv {
				if _, ok := env[k]; !ok {
					t.Fatalf("the runner step reads env %s, which this test does not give", k)
				}
			}
			output := filepath.Join(dir, "github-output")
			if err := os.WriteFile(output, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			scriptFile := filepath.Join(dir, "step.sh")
			if err := os.WriteFile(scriptFile, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bash, "--noprofile", "--norc", "-eo", "pipefail", scriptFile) // #nosec G204 -- bash is exec.LookPath("bash") and scriptFile is this test's own TempDir copy of the workflow step
			cmd.Dir = dir
			cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + dir,
				"RUNNER_TEMP=" + dir, "GITHUB_OUTPUT=" + output}
			for k, v := range env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("the step failed: %v\n%s", err, out)
			}
			got, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if want := "runner=" + c.runner + "\nrunner_docker=" + c.docker + "\n"; string(got) != want {
				t.Errorf("GITHUB_OUTPUT is\n%s\nwant\n%s\nstep output:\n%s", got, want, out)
			}
			var notices, warnings []string
			for _, line := range strings.Split(string(out), "\n") {
				if s, ok := strings.CutPrefix(line, "::notice::"); ok {
					notices = append(notices, s)
				}
				if s, ok := strings.CutPrefix(line, "::warning::"); ok {
					warnings = append(warnings, s)
				}
			}
			for _, a := range []struct {
				kind, want string
				got        []string
			}{{"notice", c.notice, notices}, {"warning", c.warning, warnings}} {
				switch {
				case a.want == "" && len(a.got) != 0:
					t.Errorf("want no %s, got %q", a.kind, a.got)
				case a.want != "" && (len(a.got) != 1 || !strings.Contains(a.got[0], a.want)):
					t.Errorf("want one %s containing %q, got %q", a.kind, a.want, a.got)
				}
			}
			if !strings.Contains(string(out), c.say) {
				t.Errorf("want the output to contain %q, got\n%s", c.say, out)
			}
			calls, err := os.ReadFile(filepath.Join(stub, "calls.log"))
			if c.noCurl && err == nil {
				t.Errorf("want no Actions API call, got\n%s", calls)
			}
			if !c.noCurl && err != nil {
				t.Errorf("want Actions API calls, got none: %v", err)
			}
		})
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
