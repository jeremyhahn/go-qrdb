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
//   - a job not on the literal `ubuntu-latest` (a waived label or ci.yml's
//     runner-selection expressions) has a step that wipes a hosted runner's
//     disk (sudo rm, docker prune), or restores a cache, not gated to the
//     hosted runner;
//   - ci.yml's playwrightJob leaves a credential in its checkout or runs an
//     apt install off the hosted runner (Company Agent m2396);
//   - an `if:` still splits on github.server_url, outside the jobs that speak
//     the Gitea API (giteaOnlyJobs) and batch.yml, whose splits are left to
//     their owner;
//   - a removed script is back in scripts/;
//   - a job has no timeout-minutes, or one over jobMinutesCeiling;
//   - an actions/cache entry is saved on any event but the nightly schedule
//     on main (CI_CACHE_SAVE), off a hosted runner, or by the combined
//     actions/cache action, which saves on every event (#1597: a push to
//     main runs on the pool, so a save gated on it never ran);
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
	// hostedRunner is the runs-on every job names unless it carries a waiver.
	hostedRunner = "ubuntu-latest"
	// ciFile is the main CI workflow, held to a floor of hosted jobs.
	ciFile = "ci.yml"
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
	// cacheSaveExpr is when a run saves the hosted Go caches: the nightly
	// schedule on main, the one main run on a hosted runner (a push to main
	// runs on the pool, #1597).
	cacheSaveExpr = "${{ github.event_name == 'schedule' && github.ref == 'refs/heads/main' }}"
	// cachePublishTerm is the conjunct every cache save's `if:` carries,
	// beside hostedStepTerm.
	cachePublishTerm = "env.CI_CACHE_SAVE == 'true'"
	// cacheSaveAction and cacheAction are the split and the combined cache
	// actions; the combined one saves in its post step on every event.
	cacheSaveAction    = "actions/cache/save@"
	cacheRestoreAction = "actions/cache/restore@"
	cacheAction        = "actions/cache@"

	// Floors, well under today's counts (7 workflows, 50 jobs).
	runnerWorkflowFloor = 6
	runnerJobFloor      = 30
	// hostedJobFloor, under ci.yml's count (19 jobs: 10 on the literal
	// ubuntu-latest, 8 on preflight's runner or runner_docker output, which
	// is the GitHub-hosted runner or the Dells' bigdisk pools by the balanced
	// selection, ruling 1137, and hosted whenever the selection cannot count;
	// preflight on its own v1.52 expression): a regression of the workflow
	// onto any other runner fails here.
	hostedJobFloor = 15

	// The three runs-on expressions of ci-template v1.53 (ruling 1087), the
	// only "${{" runs-on ci.yml may carry, byte-exact. preflightRunsOn picks
	// the bigdisk runner for this repository on github.com and ubuntu-latest
	// otherwise (v1.52); the other two read preflight's selection (v1.48,
	// ruling 1024; balanced since v1.54, ruling 1137).
	preflightRunsOn    = `${{ github.server_url == 'https://github.com' && (!startsWith(github.event_name, 'pull_request') || github.event.pull_request.head.repo.full_name == github.repository) && fromJSON(vars.CI_PREFLIGHT_RUNS_ON || '["self-hosted","linux","x64","bigdisk"]') || 'ubuntu-latest' }}`
	runnerDockerRunsOn = `${{ fromJSON(needs.preflight.outputs.runner_docker) }}`
	runnerRunsOn       = `${{ fromJSON(needs.preflight.outputs.runner || '"ubuntu-latest"') }}`
	// runnerSelectStep is preflight's step that writes runner and
	// runner_docker; its env names github.server_url.
	runnerSelectStep = "- name: Balanced runner pool (ruling 1137)\n"
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
			// Owner rulings 999 and 1006: the default.
		case file == ciFile && (runsOn == preflightRunsOn || runsOn == runnerDockerRunsOn || runsOn == runnerRunsOn):
			// ci-template v1.53 (ruling 1087): preflight's v1.52 expression
			// and the runner selection (ruling 1024; balanced, ruling 1137).
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
		}
		if runsOn != hostedRunner {
			// A waived label or ci.yml's runner-selection expressions can
			// put the job on a pool host.
			for _, s := range job.Steps {
				if hostWipeStep.MatchString(s.Run) && !strings.Contains(s.If, hostedStepTerm) {
					add("job "+name+" has a step that wipes a hosted runner's disk under if: ", strconv.Quote(s.If), ", without "+hostedStepTerm+"; on a self-hosted runner it deletes what other jobs share")
				}
				// A CI image digest record (#1609) is the one cache a pool job
				// restores; ci_image_digest_test.go holds its condition.
				if strings.HasPrefix(s.Uses, cacheRestoreAction) && !strings.Contains(s.With["path"], "ci-image-digests") && !strings.Contains(s.If, hostedStepTerm) {
					add("job "+name+" restores a cache under if: ", strconv.Quote(s.If), ", without "+hostedStepTerm+"; a pool host keeps its caches on its own disk (#1597)")
				}
			}
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
				add("job " + name + " uses the combined actions/cache, which saves on every event; restore and save apart, the save on CI_CACHE_SAVE alone")
			case strings.HasPrefix(s.Uses, cacheSaveAction) && strings.Contains(s.With["path"], "ci-image-digests"):
				// A CI image digest record (#1609), which only main's own
				// push writes; ci_image_digest_test.go holds its condition.
			case strings.HasPrefix(s.Uses, cacheSaveAction):
				usesCache = true
				if !strings.Contains(s.If, cachePublishTerm) || !strings.Contains(s.If, hostedStepTerm) || strings.Contains(s.If, "||") || strings.Contains(s.If, "env.CI_PUBLISH") {
					add("job "+name+" saves a cache under if: ", strconv.Quote(s.If), "; only the nightly schedule on main saves one, on a hosted runner ("+cachePublishTerm+" && "+hostedStepTerm+", nothing ORed)")
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
	if usesCache && wf.Env["CI_CACHE_SAVE"] != cacheSaveExpr {
		add("env.CI_CACHE_SAVE is ", strconv.Quote(wf.Env["CI_CACHE_SAVE"]), ", not ", cacheSaveExpr)
	}
	return problems, jobs
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
		// The literal hosted runner, or preflight's balanced selection
		// (ruling 1137), hosted whenever it cannot count.
		switch job.RunsOn.Value {
		case hostedRunner, runnerDockerRunsOn, runnerRunsOn:
			hosted++
		}
	}
	if hosted < hostedJobFloor {
		t.Errorf("%s has %d jobs on %s, under the floor of %d (owner rulings 999 and 1006)", ciFile, hosted, hostedRunner, hostedJobFloor)
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
	// ci-template v1.53 (ruling 1087) also names it in preflight's v1.52
	// runs-on (preflightRunsOn, exact) and in the env of the step Balanced
	// runner pool (ruling 1137), which selects no self-hosted runner off
	// github.com. Those three are stripped; any other server_url fails.
	body := readCIBody(t)
	if i := strings.Index(body, runnerLabelsStep); i >= 0 {
		if end := strings.Index(body[i:], "\n          PY\n"); end >= 0 {
			body = body[:i] + body[i+end:]
		}
	}
	body = strings.Replace(body, "runs-on: "+preflightRunsOn, "", 1)
	if i := strings.Index(body, runnerSelectStep); i >= 0 {
		if end := strings.Index(body[i:], "\n      - "); end >= 0 {
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

// runnerSelectTestsStep and lanMaskTestsStep run the behavioural tests of
// the step "Balanced runner pool (ruling 1137)" and of
// scripts/ci-mask-lan.sh in preflight (Codex round 1 on #1671).
const (
	runnerSelectTestsStep = "- name: Runner select tests (go-qrdb #1671)\n        run: python3 -I .github/workflows/testdata/runner-select-tests.py\n"
	lanMaskTestsStep      = "- name: LAN mask tests (go-qrdb #1671)\n        run: python3 -I .github/workflows/testdata/ci-mask-lan-tests.py\n"
)

func TestCIRunnerSelectAndMaskTestsPresent(t *testing.T) {
	body := readCIBody(t)
	for _, step := range []string{runnerSelectTestsStep, lanMaskTestsStep} {
		if n := strings.Count(body, step); n != 1 {
			t.Errorf("ci.yml has %d %q steps, want exactly one", n, step)
		}
	}
	for _, name := range []string{"runner-select-tests.py", "ci-mask-lan-tests.py"} {
		p := filepath.Join(repoRoot(t), ".github", "workflows", "testdata", name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("ci.yml runs .github/workflows/testdata/%s, which is missing: %v", name, err)
		}
	}
}

// hostedFile is the workflow name runnerFixture is checked as: one held to
// the literal ubuntu-latest.
const hostedFile = "release.yml"

// runnerFixture is a hosted workflow that passes every rule.
const runnerFixture = `env:
  CI_CACHE_SAVE: ${{ github.event_name == 'schedule' && github.ref == 'refs/heads/main' }}
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    steps:
      - if: runner.environment == 'github-hosted'
        uses: actions/cache/restore@0000000000000000000000000000000000000000 # v6
      - run: make build
      - if: ${{ !cancelled() && env.CI_CACHE_SAVE == 'true' && runner.environment == 'github-hosted' }}
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
		"a self-hosted label":                     {hostedFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: bigdisk", "not the literal ubuntu-latest"},
		"a runner list":                           {hostedFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: [self-hosted, linux]", "not the literal ubuntu-latest"},
		"an expression":                           {hostedFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: ${{ github.server_url == 'https://github.com' && 'ubuntu-latest' || 'batch' }}", "not the literal ubuntu-latest"},
		"a job server_url split":                  {hostedFile, "    timeout-minutes: 30\n", "    timeout-minutes: 30\n    if: github.server_url != 'https://github.com'\n", "splits on github.server_url"},
		"a step server_url split":                 {"nightly.yml", "      - run: make build", "      - if: github.server_url == 'https://github.com'\n        run: make build", "splits on github.server_url"},
		"no timeout":                              {hostedFile, "    timeout-minutes: 30\n", "", "has no timeout-minutes"},
		"a timeout over the ceiling":              {hostedFile, "timeout-minutes: 30", "timeout-minutes: 351", "not a number of minutes from 1 to 350"},
		"a matrix leg over the ceiling":           {hostedFile, "minutes: 350", "minutes: 360", "not a number of minutes from 1 to 350"},
		"a matrix limit nothing sets":             {hostedFile, "timeout-minutes: ${{ matrix.minutes }}", "timeout-minutes: ${{ matrix.limit }}", "which no matrix entry sets"},
		"a cache saved on every event":            {hostedFile, "${{ !cancelled() && env.CI_CACHE_SAVE == 'true' && runner.environment == 'github-hosted' }}", "${{ !cancelled() }}", "only the nightly schedule on main saves one"},
		"a cache saved on a push":                 {hostedFile, "env.CI_CACHE_SAVE == 'true' && runner", "env.CI_PUBLISH == 'true' && runner", "only the nightly schedule on main saves one"},
		"a cache saved off hosted":                {hostedFile, " && runner.environment == 'github-hosted' }}\n        uses: actions/cache/save", " }}\n        uses: actions/cache/save", "only the nightly schedule on main saves one"},
		"a cache save widened":                    {hostedFile, "env.CI_CACHE_SAVE == 'true' && runner", "(env.CI_CACHE_SAVE == 'true' || github.event_name == 'pull_request') && runner", "only the nightly schedule on main saves one"},
		"the combined cache action":               {hostedFile, "actions/cache/restore@", "actions/cache@", "combined actions/cache"},
		"a save flag on every schedule":           {hostedFile, "github.event_name == 'schedule' && github.ref == 'refs/heads/main'", "github.event_name == 'schedule'", "env.CI_CACHE_SAVE is"},
		"a save flag on a pull request":           {hostedFile, "github.event_name == 'schedule' && github.ref == 'refs/heads/main'", "github.event_name == 'pull_request'", "env.CI_CACHE_SAVE is"},
		"a written GOMAXPROCS":                    {hostedFile, "make leg", "GOMAXPROCS=4 make leg", "GOMAXPROCS or -p count"},
		"a written -p":                            {hostedFile, "make leg", "go test -p 4 ./...", "GOMAXPROCS or -p count"},
		"a GOMAXPROCS step env":                   {hostedFile, "      - run: make leg", "      - run: make leg\n        env:\n          GOMAXPROCS: \"8\"", "sets GOMAXPROCS in a step's env"},
		"a self-hosted label in ci.yml":           {ciFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: bigdisk", "carries no `# self-hosted: <category>: <reason>` waiver"},
		"an expression with a waiver":             {ciFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: ${{ 'bigdisk' }}  # self-hosted: oversize: a fixture reason", "not the literal ubuntu-latest nor one self-hosted label"},
		"a review label in ci.yml":                {ciFile, "  build:\n    runs-on: ubuntu-latest", "  build:\n    runs-on: codex  # self-hosted: hardware: a fixture reason", "label is reserved to code-review.yml's review job"},
		"a disk wipe on a waived job":             {hostedFile, "  build:\n    runs-on: ubuntu-latest\n    timeout-minutes: 30\n    steps:\n", "  build:\n    runs-on: gpu  # self-hosted: hardware: a fixture reason\n    timeout-minutes: 30\n    steps:\n      - run: sudo rm -rf /usr/share/dotnet\n", "wipes a hosted runner's disk"},
		"a cache restore on a pool host":          {hostedFile, "  build:\n    runs-on: ubuntu-latest\n    timeout-minutes: 30\n    steps:\n      - if: runner.environment == 'github-hosted'\n", "  build:\n    runs-on: gpu  # self-hosted: hardware: a fixture reason\n    timeout-minutes: 30\n    steps:\n      - if: always()\n", "restores a cache"},
		"a disk wipe on an overflow job":          {ciFile, "  build:\n    runs-on: ubuntu-latest\n    timeout-minutes: 30\n    steps:\n", "  build:\n    runs-on: " + runnerDockerRunsOn + "\n    timeout-minutes: 30\n    steps:\n      - run: sudo rm -rf /usr/share/dotnet\n", "wipes a hosted runner's disk"},
		"a cache restore on an overflow job":      {ciFile, "  build:\n    runs-on: ubuntu-latest\n    timeout-minutes: 30\n    steps:\n      - if: runner.environment == 'github-hosted'\n", "  build:\n    runs-on: " + runnerDockerRunsOn + "\n    timeout-minutes: 30\n    steps:\n      - if: always()\n", "restores a cache"},
		"a disk wipe on a hosted-or-overflow job": {ciFile, "  build:\n    runs-on: ubuntu-latest\n    timeout-minutes: 30\n    steps:\n", "  build:\n    runs-on: " + runnerRunsOn + "\n    timeout-minutes: 30\n    steps:\n      - run: sudo rm -rf /usr/share/dotnet\n", "wipes a hosted runner's disk"},
		"a disk wipe on preflight's runner":       {ciFile, "  build:\n    runs-on: ubuntu-latest\n    timeout-minutes: 30\n    steps:\n", "  build:\n    runs-on: " + preflightRunsOn + "\n    timeout-minutes: 30\n    steps:\n      - run: sudo rm -rf /usr/share/dotnet\n", "wipes a hosted runner's disk"},
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

// TestCIOverflowJobGuardsCanFail drops the hosted-runner guard from a real
// ci.yml step of an overflow job (one whose runner-selection expression can
// land it on a pool host) and wants the gate to name the job.
func TestCIOverflowJobGuardsCanFail(t *testing.T) {
	body := string(workflowFiles(t)[ciFile])
	rules := []string{"wipes a hosted runner's disk", "restores a cache"}
	problems, _ := runnerProblems(ciFile, []byte(body))
	for _, p := range problems {
		for _, r := range rules {
			if strings.Contains(p, r) {
				t.Fatalf("%s already fails the %q rule: %s", ciFile, r, p)
			}
		}
	}
	cases := map[string]struct {
		old, new, job, want string
	}{
		"docker-build's disk wipe": {"        if: runner.environment == 'github-hosted'\n", "        if: always()\n", "docker-build", rules[0]},
		"lint's cache restore":     {"        if: ${{ runner.environment == 'github-hosted' }}\n        uses: actions/cache/restore@", "        uses: actions/cache/restore@", "lint", rules[1]},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			at := strings.Index(body, c.old)
			if at < 0 {
				t.Fatalf("%s lacks %q", ciFile, c.old)
			}
			job := jobAtLine(t, body, strings.Count(body[:at], "\n")+1)
			if job != c.job {
				t.Fatalf("the first %q in %s is in job %s, not %s", c.old, ciFile, job, c.job)
			}
			mutated := body[:at] + c.new + body[at+len(c.old):]
			problems, _ := runnerProblems(ciFile, []byte(mutated))
			if !slices.ContainsFunc(problems, func(p string) bool {
				return strings.Contains(p, "job "+job+" ") && strings.Contains(p, c.want)
			}) {
				t.Fatalf("want a problem naming job %s and containing %q, got %v", job, c.want, problems)
			}
		})
	}
}

// jobAtLine returns the name of the workflow job whose block holds the
// 1-based line, and fails unless that job runs on one of ci.yml's
// runner-selection expressions.
func jobAtLine(t *testing.T, body string, line int) string {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil || len(doc.Content) == 0 {
		t.Fatalf("parse %s: %v", ciFile, err)
	}
	var jobs *yaml.Node
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "jobs" {
			jobs = root.Content[i+1]
		}
	}
	if jobs == nil {
		t.Fatalf("%s has no jobs", ciFile)
	}
	name, runsOn := "", ""
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		if jobs.Content[i].Line > line {
			break
		}
		name, runsOn = jobs.Content[i].Value, ""
		def := jobs.Content[i+1]
		for j := 0; j+1 < len(def.Content); j += 2 {
			if def.Content[j].Value == "runs-on" {
				runsOn = def.Content[j+1].Value
			}
		}
	}
	switch runsOn {
	case preflightRunsOn, runnerDockerRunsOn, runnerRunsOn:
	default:
		t.Fatalf("line %d of %s is in job %q, which runs on %q, not a runner-selection expression", line, ciFile, name, runsOn)
	}
	return name
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
