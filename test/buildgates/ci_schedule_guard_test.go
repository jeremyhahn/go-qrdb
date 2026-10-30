package buildgates

// ci-template v1.29.3 (Company Agent ruling 255(10)/(11)(c)): one schedule
// run per repository, skipped when a main run is in flight. The
// schedule-guard job sets `verdict` (run, busy or unreadable) on a schedule
// run; every bigdisk job runs only on `run`, and CI Gate skips on `busy` and
// fails on `unreadable`. Here every bigdisk job needs docker-build, so the
// guard clause is on docker-build (and on soak, as the template's soak carries
// it) and the rest skip with
// docker-build. These tests hold that: a bigdisk job is guarded itself, or
// needs a guarded job and has no status function in its `if:` that would run
// it past a skipped need.

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	scheduleGuardJob = "schedule-guard"
	// scheduleGuardClause is the conjunct a guarded job's `if:` ends with.
	scheduleGuardClause = "(github.event_name != 'schedule' || needs." + scheduleGuardJob + ".outputs.verdict == 'run')"
	// scheduleGuardGateIf is CI Gate's `if:`: it skips on `busy` alone.
	scheduleGuardGateIf = "${{ always() && (github.event_name != 'schedule' || needs." + scheduleGuardJob + ".outputs.verdict != 'busy') }}"
	// scheduleGuardBot is the guard step's fallback token: AGENT_TOKEN on a
	// schedule run, nothing on any other event.
	scheduleGuardBot = "${{ github.event_name == 'schedule' && secrets.AGENT_TOKEN || '' }}"
	// bigdiskLabel is how a job's runs-on names the bigdisk runners.
	bigdiskLabel = "'bigdisk'"
	// minBigdiskJobs is the discovery floor: every bigdisk assertion is in a
	// loop, and an empty loop reports success.
	minBigdiskJobs = 12
)

// statusFunctions run a job past a need that skipped, so a job whose `if:`
// carries one does not skip with docker-build.
var statusFunctions = []string{"always()", "cancelled()", "failure()"}

type scheduleGuardWorkflow struct {
	Jobs map[string]struct {
		RunsOn      string            `yaml:"runs-on"`
		If          string            `yaml:"if"`
		Needs       yaml.Node         `yaml:"needs"`
		Permissions map[string]string `yaml:"permissions"`
		Outputs     map[string]string `yaml:"outputs"`
		Steps       []struct {
			ID   string            `yaml:"id"`
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// tierCondition is a job's `if:` as the tier checks read it: without the
// draft skip and the schedule guard clause, neither of which is a tier.
func tierCondition(cond string) string {
	return withoutDraftSkip(withoutScheduleGuard(cond))
}

// withoutScheduleGuard drops a trailing schedule guard clause from cond.
func withoutScheduleGuard(cond string) string {
	c := strings.TrimSpace(cond)
	if s, ok := strings.CutSuffix(c, " && "+scheduleGuardClause+" }}"); ok {
		return s + " }}"
	}
	return c
}

func readCIBody(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	return string(b)
}

func parseScheduleGuardWorkflow(t *testing.T, body string) scheduleGuardWorkflow {
	t.Helper()
	var wf scheduleGuardWorkflow
	if err := yaml.Unmarshal([]byte(body), &wf); err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}
	return wf
}

// scheduleGuardProblems checks the guard job, the bigdisk jobs and CI Gate
// against ci-template v1.29.3.
func scheduleGuardProblems(wf scheduleGuardWorkflow) []string {
	var problems []string
	guard, ok := wf.Jobs[scheduleGuardJob]
	if !ok {
		return []string{"ci.yml has no " + scheduleGuardJob + " job: a schedule run would start its bigdisk jobs while main is busy"}
	}
	if guard.RunsOn != "ubuntu-latest" {
		problems = append(problems, scheduleGuardJob+" runs on "+guard.RunsOn+", not ubuntu-latest")
	}
	if n := needsList(guard.Needs); len(n) != 0 {
		problems = append(problems, scheduleGuardJob+" needs "+strings.Join(n, ",")+": it must start with the run")
	}
	if guard.Permissions["actions"] != "read" || guard.Permissions["contents"] != "read" || len(guard.Permissions) != 2 {
		problems = append(problems, scheduleGuardJob+"'s permissions are not exactly actions: read and contents: read")
	}
	if guard.Outputs["verdict"] != "${{ steps.guard.outputs.verdict }}" {
		problems = append(problems, scheduleGuardJob+" has no verdict output from its guard step")
	}
	for _, s := range guard.Steps {
		if s.Uses != "" {
			problems = append(problems, scheduleGuardJob+" uses "+s.Uses+": it checks nothing out, so no repository code runs beside the tokens")
		}
		if s.ID == "guard" && s.Env["BOT"] != scheduleGuardBot {
			problems = append(problems, scheduleGuardJob+"'s BOT is "+s.Env["BOT"]+", not "+scheduleGuardBot+": AGENT_TOKEN reaches it on a schedule run only")
		}
	}

	names := slices.Sorted(maps.Keys(wf.Jobs))
	covered := map[string]bool{}
	for _, name := range names {
		job := wf.Jobs[name]
		if strings.Contains(job.If, scheduleGuardClause) {
			if !slices.Contains(needsList(job.Needs), scheduleGuardJob) {
				problems = append(problems, "job "+name+"'s `if:` reads the schedule guard but it does not need "+scheduleGuardJob)
				continue
			}
			covered[name] = true
		}
	}
	// A job skips with a need that skipped unless a status function in its
	// `if:` runs it anyway; iterate to the fixed point over the needs graph.
	for changed := true; changed; {
		changed = false
		for _, name := range names {
			job := wf.Jobs[name]
			if covered[name] || slices.ContainsFunc(statusFunctions, func(f string) bool { return strings.Contains(job.If, f) }) {
				continue
			}
			if slices.ContainsFunc(needsList(job.Needs), func(d string) bool { return covered[d] }) {
				covered[name], changed = true, true
			}
		}
	}
	bigdisk := 0
	for _, name := range names {
		if !strings.Contains(wf.Jobs[name].RunsOn, bigdiskLabel) {
			continue
		}
		bigdisk++
		if !covered[name] {
			problems = append(problems, "bigdisk job "+name+" neither carries the schedule guard clause nor needs a job that does: "+
				"a schedule run would start it while main is busy (ruling 255(10)/(11)(c))")
		}
	}
	if bigdisk < minBigdiskJobs {
		problems = append(problems, "ci.yml has "+strconv.Itoa(bigdisk)+" bigdisk jobs; want at least "+strconv.Itoa(minBigdiskJobs))
	}

	gate, ok := wf.Jobs[ciGateJob]
	if !ok {
		return append(problems, "ci.yml has no "+ciGateJob+" job")
	}
	if gate.If != scheduleGuardGateIf {
		problems = append(problems, ciGateJob+"'s `if:` is "+gate.If+", not "+scheduleGuardGateIf+": a busy nightly would post a green gate")
	}
	if !slices.Contains(needsList(gate.Needs), scheduleGuardJob) {
		problems = append(problems, ciGateJob+" does not need "+scheduleGuardJob)
	}
	found := false
	for _, s := range gate.Steps {
		if strings.Contains(s.Run, "check()") {
			found = true
			if s.Env["SCHEDULE"] != "${{ github.event_name == 'schedule' }}" ||
				s.Env["SCHEDULE_GUARD"] != "${{ needs."+scheduleGuardJob+".outputs.verdict }}" {
				problems = append(problems, ciGateJob+"'s step does not read SCHEDULE and SCHEDULE_GUARD from the event and the guard's verdict")
			}
		}
	}
	if !found {
		problems = append(problems, ciGateJob+" has no step defining check()")
	}
	return problems
}

// TestCIScheduleGuardHoldsEveryBigdiskJob pins ci-template v1.29.3's guard.
func TestCIScheduleGuardHoldsEveryBigdiskJob(t *testing.T) {
	for _, p := range scheduleGuardProblems(parseScheduleGuardWorkflow(t, readCIBody(t))) {
		t.Error(p)
	}
}

// TestScheduleGuardProblems_CanFail shows each check reports its defect.
func TestScheduleGuardProblems_CanFail(t *testing.T) {
	body := readCIBody(t)
	cases := []struct {
		name, old, new, want string
	}{
		{"docker-build unguarded",
			"if: ${{ !github.event.pull_request.draft && " + scheduleGuardClause + " }}",
			"if: ${{ !github.event.pull_request.draft }}",
			"bigdisk job docker-build"},
		{"a dependent runs past a skipped docker-build",
			"if: ${{ !github.event.pull_request.draft }}   # ruling 214: a draft PR runs no CI\n    name: Devcontainer image [lint",
			"if: ${{ always() && !github.event.pull_request.draft }}\n    name: Devcontainer image [lint",
			"bigdisk job lint "},
		{"guard clause without the need",
			"needs: [changes, schedule-guard]   # v1.29.3",
			"needs: [changes]",
			"docker-build's `if:` reads the schedule guard"},
		{"gate posts green on busy",
			"if: " + scheduleGuardGateIf,
			"if: always()",
			ciGateJob + "'s `if:`"},
		{"bot token on every event",
			"BOT: " + scheduleGuardBot,
			"BOT: ${{ secrets.AGENT_TOKEN }}",
			scheduleGuardJob + "'s BOT"},
		{"guard needs another job",
			"  schedule-guard:\n",
			"  schedule-guard:\n    needs: [changes]\n",
			scheduleGuardJob + " needs changes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Count(body, tc.old) != 1 {
				t.Fatalf("fixture: ci.yml holds %q %d times, want once", tc.old, strings.Count(body, tc.old))
			}
			problems := scheduleGuardProblems(parseScheduleGuardWorkflow(t, strings.Replace(body, tc.old, tc.new, 1)))
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, tc.want) }) {
				t.Fatalf("no problem names %q; got %v", tc.want, problems)
			}
		})
	}
}

// TestScheduleGuardProblems_SoakCanFail proves the gate catches an unguarded
// soak. soak needs docker-build as well as carrying the guard clause, so it is
// unguarded only when it loses both its own clause and the cover docker-build
// gives it: each case makes two edits.
func TestScheduleGuardProblems_SoakCanFail(t *testing.T) {
	body := readCIBody(t)
	soakClause := [2]string{
		"workflow_dispatch') && !github.event.pull_request.draft && " + scheduleGuardClause,
		"workflow_dispatch') && !github.event.pull_request.draft",
	}
	cases := []struct {
		name  string
		edits [][2]string
	}{
		{"soak unguarded, off docker-build", [][2]string{
			soakClause,
			{"needs: [schedule-guard, docker-build]", "needs: [schedule-guard]"},
		}},
		{"soak and docker-build both unguarded", [][2]string{
			soakClause,
			{"if: ${{ !github.event.pull_request.draft && " + scheduleGuardClause + " }}", "if: ${{ !github.event.pull_request.draft }}"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := body
			for _, e := range tc.edits {
				if strings.Count(mutated, e[0]) != 1 {
					t.Fatalf("fixture: ci.yml holds %q %d times, want once", e[0], strings.Count(mutated, e[0]))
				}
				mutated = strings.Replace(mutated, e[0], e[1], 1)
			}
			problems := scheduleGuardProblems(parseScheduleGuardWorkflow(t, mutated))
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, "bigdisk job soak") }) {
				t.Fatalf("no problem names bigdisk job soak; got %v", problems)
			}
		})
	}
}

func TestWithoutScheduleGuard(t *testing.T) {
	cases := map[string]string{
		"${{ !github.event.pull_request.draft && " + scheduleGuardClause + " }}":             "${{ !github.event.pull_request.draft }}",
		"${{ (a || b) && !github.event.pull_request.draft && " + scheduleGuardClause + " }}": "${{ (a || b) && !github.event.pull_request.draft }}",
		"${{ !github.event.pull_request.draft }}":                                            "${{ !github.event.pull_request.draft }}",
		"${{ " + scheduleGuardClause + " && x }}":                                            "${{ " + scheduleGuardClause + " && x }}",
		"": "",
	}
	for in, want := range cases {
		if got := withoutScheduleGuard(in); got != want {
			t.Errorf("withoutScheduleGuard(%q) = %q, want %q", in, got, want)
		}
	}
	if got := tierCondition("${{ !github.event.pull_request.draft && " + scheduleGuardClause + " }}"); got != "" {
		t.Errorf("tierCondition of a guarded PR-tier `if:` = %q, want empty", got)
	}
}

// TestCIGateScriptFailsClosedOnTheScheduleGuard runs CI Gate's script with
// each verdict a schedule run can reach it with.
func TestCIGateScriptFailsClosedOnTheScheduleGuard(t *testing.T) {
	run, env, ok := gateScript(readCIWorkflow(t))
	if !ok {
		t.Fatalf("ci.yml has no %s step defining check()", ciGateJob)
	}
	cases := []struct {
		name, schedule, verdict string
		results                 map[string]string
		wantExit                bool
	}{
		{"schedule run, verdict run", "true", "run", nil, false},
		{"schedule run, verdict unreadable", "true", "unreadable", nil, true},
		{"schedule run, no verdict", "true", "", nil, true},
		{"schedule run, verdict busy", "true", "busy", nil, true},
		{"schedule run, guard failed", "true", "run", map[string]string{scheduleGuardJob: "failure"}, true},
		{"dispatch, verdict run", "false", "run", nil, false},
		{"dispatch, no verdict", "false", "", nil, false},
		{"dispatch, guard skipped", "false", "run", map[string]string{scheduleGuardJob: "skipped"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exit := runGate(t, run, env, tc.results, "true", "true", "false",
				"SCHEDULE="+tc.schedule, "SCHEDULE_GUARD="+tc.verdict)
			if tc.wantExit && exit == 0 {
				t.Fatalf("%s passed; want it to fail", ciGateJob)
			}
			if !tc.wantExit && exit != 0 {
				t.Fatalf("%s failed with exit %d; want it to pass", ciGateJob, exit)
			}
		})
	}
}

// fakeCurl stands in for curl in the guard step: it answers the runs route
// with FAKE_CODE_<token> (default 200), FAKE_BODY_<token> or a total_count of
// FAKE_COUNT_<status>, exits FAKE_EXIT_<token> when set, and logs each call's
// token and its argv apart, so a test can see which token was used and that
// none reached the command line.
const fakeCurl = `#!/usr/bin/env bash
argv="$*" out= hdr= url=
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift 2;;
    -w|--max-time) shift 2;;
    -H) hdr=$(cat "${2#@}"); shift 2;;
    -*) shift;;
    *) url=$1; shift;;
  esac
done
tok=${hdr#Authorization: token }
st=${url##*status=}; st=${st%%&*}
printf '%s\n' "$argv" >> "$FAKE_ARGV"
printf '%s %s\n' "$tok" "$url" >> "$FAKE_LOG"
v=FAKE_EXIT_$tok; [ -z "${!v:-}" ] || exit "${!v}"
v=FAKE_CODE_$tok; code=${!v:-200}
[ "$code" = 200 ] || { printf '%s' "$code"; exit 0; }
v=FAKE_BODY_$tok
if [ -n "${!v:-}" ]; then printf '%s' "${!v}" > "$out"; else
  v=FAKE_COUNT_$st; printf '{"total_count":%s,"workflow_runs":[]}' "${!v:-0}" > "$out"
fi
printf 200
`

// TestScheduleGuardStepVerdicts runs the guard step's own script against a
// fake curl, with the real jq, over every path to a verdict.
func TestScheduleGuardStepVerdicts(t *testing.T) {
	wf := parseScheduleGuardWorkflow(t, readCIBody(t))
	var script string
	for _, s := range wf.Jobs[scheduleGuardJob].Steps {
		if s.ID == "guard" {
			script = s.Run
		}
	}
	if script == "" {
		t.Fatalf("%s has no step with id guard", scheduleGuardJob)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash is required to execute the guard step: %v", err)
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatalf("jq is required to execute the guard step (the toolchain image installs it): %v", err)
	}
	const api = "https://forge.example.com/api/v1/repos/o/r/actions/workflows/ci.yml/runs"
	cases := []struct {
		name        string
		event       string
		server      string
		jobToken    string
		bot         string
		fake        []string
		wantVerdict string
		wantTokens  []string // the token of each call, in order
	}{
		{"push: no check", "push", "https://forge.example.com", "jobtok", "", nil, "run", nil},
		{"github.com mirror: no check", "schedule", "https://github.com", "jobtok", "bottok", nil, "run", nil},
		{"main idle", "schedule", "https://forge.example.com", "jobtok", "bottok", nil, "run", []string{"jobtok", "jobtok"}},
		{"main push run in progress", "schedule", "https://forge.example.com", "jobtok", "bottok",
			[]string{"FAKE_COUNT_in_progress=1"}, "busy", []string{"jobtok", "jobtok"}},
		{"main push run waiting", "schedule", "https://forge.example.com", "jobtok", "bottok",
			[]string{"FAKE_COUNT_waiting=2"}, "busy", []string{"jobtok", "jobtok"}},
		{"job token refused, AGENT_TOKEN reads", "schedule", "https://forge.example.com", "jobtok", "bottok",
			[]string{"FAKE_CODE_jobtok=403"}, "run", []string{"jobtok", "bottok", "bottok"}},
		{"job token refused, AGENT_TOKEN reads busy", "schedule", "https://forge.example.com", "jobtok", "bottok",
			[]string{"FAKE_CODE_jobtok=403", "FAKE_COUNT_waiting=1"}, "busy", []string{"jobtok", "bottok", "bottok"}},
		{"job token gets no count, AGENT_TOKEN reads", "schedule", "https://forge.example.com", "jobtok", "bottok",
			[]string{"FAKE_BODY_jobtok={}"}, "run", []string{"jobtok", "bottok", "bottok"}},
		{"no job token, AGENT_TOKEN reads", "schedule", "https://forge.example.com", "", "bottok",
			nil, "run", []string{"bottok", "bottok"}},
		{"job token refused, no AGENT_TOKEN", "schedule", "https://forge.example.com", "jobtok", "",
			[]string{"FAKE_CODE_jobtok=403"}, "unreadable", []string{"jobtok"}},
		{"both refused", "schedule", "https://forge.example.com", "jobtok", "bottok",
			[]string{"FAKE_CODE_jobtok=403", "FAKE_CODE_bottok=401"}, "unreadable", []string{"jobtok", "bottok"}},
		{"curl fails for both", "schedule", "https://forge.example.com", "jobtok", "bottok",
			[]string{"FAKE_EXIT_jobtok=7", "FAKE_EXIT_bottok=28"}, "unreadable", []string{"jobtok", "bottok"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(fakeCurl), 0o700); err != nil { // #nosec G306 -- the fake must be executable
				t.Fatal(err)
			}
			out := filepath.Join(dir, "output")
			logPath := filepath.Join(dir, "calls")
			argvPath := filepath.Join(dir, "argv")
			vars := append([]string{
				"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"EVENT=" + tc.event, "SERVER=" + tc.server, "RUNS_API=" + api,
				"JOB_TOKEN=" + tc.jobToken, "BOT=" + tc.bot,
				"RUNNER_TEMP=" + dir, "GITHUB_OUTPUT=" + out,
				"FAKE_LOG=" + logPath, "FAKE_ARGV=" + argvPath,
			}, tc.fake...)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-eo", "pipefail", "-c", script) // #nosec G204 -- the script is ci.yml's own guard step
			cmd.Env = vars
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("guard step failed: %v\n%s", err, b)
			}
			got, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("read GITHUB_OUTPUT: %v", err)
			}
			if want := "verdict=" + tc.wantVerdict + "\n"; string(got) != want {
				t.Fatalf("GITHUB_OUTPUT = %q, want %q", got, want)
			}
			calls, _ := os.ReadFile(logPath) // absent when no call was made
			var tokens []string
			for line := range strings.Lines(string(calls)) {
				tok, url, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
				if !strings.HasPrefix(url, api+"?event=push&branch=main&status=") {
					t.Fatalf("call to %q, not this workflow's main push runs", url)
				}
				tokens = append(tokens, tok)
			}
			if !slices.Equal(tokens, tc.wantTokens) {
				t.Fatalf("calls used tokens %v, want %v", tokens, tc.wantTokens)
			}
			argv, _ := os.ReadFile(argvPath)
			for _, tok := range []string{"jobtok", "bottok"} {
				if strings.Contains(string(argv), tok) {
					t.Fatalf("token %s reached curl's command line: %s", tok, argv)
				}
			}
		})
	}
}
