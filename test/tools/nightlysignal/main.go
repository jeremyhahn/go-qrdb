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

// Command nightlysignal turns the verdict of a nightly workflow into a tracking
// issue on this repository (go-qrdb#478).
//
// THE PROBLEM IT SOLVES, MEASURED. On 2026-09-12 the API retained 22 runs of
// nightly.yml, going back to 2026-08-22, and EVERY ONE OF THEM WAS RED. One of
// them — run 16597 — had correctly caught CVE-2026-84445 against
// google.golang.org/grpc in all three go.mod files, on a database downloaded
// that morning. Nobody saw it. The advisory reached this repository only
// because an agent happened to run `make security` on a workstation a day
// later. conventions/devops.md §8a accepts a window in alpha — a CVE surfaces
// at the next nightly rather than at the push that introduces it — and that
// bargain assumes the nightly's verdict is READ.
//
// WHY NOT SIMPLY A NOTIFICATION. Because a notifier that fires on every one of
// twenty-two consecutive failures trains its reader to ignore it inside a week,
// and then the mechanism is intact while the property is gone. That is the same
// defect as a red check everyone has learned to disregard, in a new place. So
// the unit of signal here is a STATE, not an event:
//
//	red, nothing open      -> open an issue                (the notification)
//	red, issue open        -> rewrite that issue's body    (silent; no bell)
//	green, issue open      -> comment and close it         (the good news)
//	green, nothing open    -> do nothing
//	cancelled or skipped   -> do nothing, and say so
//
// A new issue in the queue therefore means something BROKE. An old open one
// means it is still broken. The two are distinguishable at a glance, which is
// the property a nightly notification has to have to survive contact with a
// durably-red job.
//
// WHY AN ISSUE. AGENTS.md names this repository's open issues as the inbound
// work queue — "check them at session start" — so it is a channel that is
// already read, rather than a new one nobody has subscribed to. It needs no
// external service, no credential beyond the project agent's own token, and no
// per-developer configuration. Open/update/close gives newly-red versus
// still-red for free, and the issue's own history is the record of how long the
// job was broken.
//
// A JOB THAT HAS NEVER BEEN GREEN IS LOUDER. It is a different condition from
// one that just broke: it means nothing has ever been in a position to notice,
// and the absence of a green run was itself the information. It gets its own
// title, its own note, and priority:critical rather than priority:high.
//
// WHAT IT DOES NOT DO. It does not make the nightly a required check, and it
// does not move `trivy fs` onto the push path — that is a separate decision
// (the phase rule in conventions/devops.md §8a).
//
// EXIT STATUS IS ABOUT THIS TOOL, NOT ABOUT THE NIGHTLY. It exits 0 when every
// job was decided and every decision applied, INCLUDING when the answer was
// "everything is on fire". It exits non-zero only when it could not do its own
// job — which is the only way a silent notifier can be told from a quiet night.
//
// Usage:
//
//	nightlysignal -mode report|apply -repo owner/name -api URL \
//	  -ref REF -run-id N -workflow-file PATH -self KEY \
//	  -job 'key=Display Name=result' [-job ...]
//
// The token comes from NIGHTLY_SIGNAL_TOKEN.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "nightlysignal: "+err.Error())
		os.Exit(1)
	}
}

// Modes. report does every read and every decision and writes nothing, which is
// what `make nightly-signal` runs; apply is what the workflow runs.
const (
	modeReport = "report"
	modeApply  = "apply"
)

// jobList collects repeated -job flags.
type jobList []JobSpec

func (l *jobList) String() string {
	names := make([]string, 0, len(*l))
	for _, s := range *l {
		names = append(names, s.Key)
	}
	return strings.Join(names, ",")
}

// Set parses `key=Display Name=result`.
//
// Three fields split on the FIRST TWO `=` only, so a display name may contain
// one. Explicit rather than derived from the workflow file because
// conventions/devops.md §1 rules that a gate must list every job explicitly and
// fail closed when the results cannot be read: the list is the declaration, and
// AssertCoverage is what makes forgetting an entry fail rather than pass.
func (l *jobList) Set(v string) error {
	parts := strings.SplitN(v, "=", 3)
	if len(parts) != 3 {
		return &JobSpecError{Spec: v, Why: "expected key=Display Name=result"}
	}
	for i, p := range parts {
		if strings.TrimSpace(p) == "" {
			return &JobSpecError{Spec: v, Why: "field " + strconv.Itoa(i+1) + " is empty"}
		}
	}
	*l = append(*l, JobSpec{Key: parts[0], Name: parts[1], Result: parts[2]})
	return nil
}

type config struct {
	mode          string
	repo          string
	api           string
	ref           string
	defaultBranch string
	runID         int64
	workflow      string
	workflowFile  string
	self          string
	jobs          jobList
	historyLimit  int
	maxIssuePages int
	token         string
}

func parseFlags(args []string, out io.Writer) (config, error) {
	var cfg config
	fs := flag.NewFlagSet("nightlysignal", flag.ContinueOnError)
	fs.SetOutput(out)

	server := strings.TrimRight(os.Getenv("GITHUB_SERVER_URL"), "/")
	defaultAPI := ""
	if server != "" {
		defaultAPI = server + "/api/v1"
	}
	defaultRunID, _ := strconv.ParseInt(os.Getenv("GITHUB_RUN_ID"), 10, 64)

	fs.StringVar(&cfg.mode, "mode", modeReport, "report (decide and print) or apply (decide and write)")
	fs.StringVar(&cfg.repo, "repo", os.Getenv("GITHUB_REPOSITORY"), "owner/name")
	fs.StringVar(&cfg.api, "api", defaultAPI, "API root, e.g. https://forge.example/api/v1")
	fs.StringVar(&cfg.ref, "ref", os.Getenv("GITHUB_REF_NAME"), "the git ref this run is about")
	fs.StringVar(&cfg.defaultBranch, "default-branch", "main", "the ref whose tracker needs no ref in its title")
	fs.Int64Var(&cfg.runID, "run-id", defaultRunID, "this run's database id (GITHUB_RUN_ID)")
	fs.StringVar(&cfg.workflow, "workflow", "nightly.yml", "the workflow file name the runs API knows")
	fs.StringVar(&cfg.workflowFile, "workflow-file", ".github/workflows/nightly.yml", "path to that file in the tree")
	fs.StringVar(&cfg.self, "self", "signal", "the YAML key of the signal job itself")
	fs.Var(&cfg.jobs, "job", "a watched job as key=Display Name=result; repeat once per job")
	fs.IntVar(&cfg.historyLimit, "history-limit", 50, "how many runs of the workflow to list when looking for the last green one")
	fs.IntVar(&cfg.maxIssuePages, "max-issue-pages", 20, "refuse rather than stop short when the open-issue listing is longer than this")

	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	cfg.token = os.Getenv("NIGHTLY_SIGNAL_TOKEN")
	return cfg, cfg.validate()
}

func (c config) validate() error {
	if c.mode != modeReport && c.mode != modeApply {
		return &ConfigError{What: "-mode", Why: "must be " + modeReport + " or " + modeApply + ", got " + strconv.Quote(c.mode)}
	}
	if c.repo == "" || !strings.Contains(c.repo, "/") {
		return &ConfigError{What: "-repo", Why: "must be owner/name (GITHUB_REPOSITORY supplies it in a workflow)"}
	}
	if c.api == "" {
		return &ConfigError{What: "-api", Why: "is empty and GITHUB_SERVER_URL is unset, so there is nothing to talk to"}
	}
	if c.ref == "" {
		return &ConfigError{What: "-ref", Why: "is empty; the tracker is keyed by ref, and an empty one would collide across branches"}
	}
	if len(c.jobs) == 0 {
		return &ConfigError{What: "-job", Why: "no job was passed, so this run would report success having watched nothing"}
	}
	// The token gates READS as well as writes: this repository is private, so
	// an anonymous listing answers 404 and an existing tracker would look
	// absent — which opens a duplicate every night.
	if c.token == "" {
		return &ConfigError{What: "NIGHTLY_SIGNAL_TOKEN", Why: "is empty; every route this tool uses needs it, and an anonymous read of a private repo looks like an empty repo"}
	}
	if c.mode == modeApply && c.runID == 0 {
		return &ConfigError{What: "-run-id", Why: "is zero in apply mode; the issue must name the run it came from"}
	}
	if c.historyLimit < 1 {
		return &ConfigError{What: "-history-limit", Why: "must be at least 1"}
	}
	if c.maxIssuePages < 1 {
		return &ConfigError{What: "-max-issue-pages", Why: "must be at least 1"}
	}
	return nil
}

func run(args []string, out io.Writer) error {
	cfg, err := parseFlags(args, out)
	if err != nil {
		return err
	}

	declared, err := DeclaredJobs(cfg.workflowFile)
	if err != nil {
		return err
	}
	if err := AssertCoverage(cfg.workflowFile, declared, cfg.jobs, cfg.self); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	return decideAll(ctx, cfg, NewForge(cfg.api, cfg.repo, cfg.token), out)
}

// outcome is one job's decision, for the run summary.
type outcome struct {
	job        JobSpec
	verdict    Verdict
	action     Action
	provenance Provenance
	issue      int64
	note       string
}

func decideAll(ctx context.Context, cfg config, f *Forge, out io.Writer) error {
	current := currentRun(ctx, cfg, f)

	issues, err := f.OpenIssues(ctx, cfg.maxIssuePages)
	if err != nil {
		return err
	}

	// Fetched once for every job rather than per job: it is the same call, and
	// a failure to read it must not stop the decisions — it only costs the
	// excerpt, which Excerpt.Render then says out loud.
	currentJobs, jobsErr := f.RunJobs(ctx, cfg.runID)

	var problems []error
	outcomes := make([]outcome, 0, len(cfg.jobs))
	for _, spec := range cfg.jobs {
		result, err := decideOne(ctx, cfg, f, spec, current, issues, currentJobs, jobsErr)
		if err != nil {
			problems = append(problems, err)
		}
		outcomes = append(outcomes, result)
	}

	writeSummary(out, cfg, current, outcomes)
	return errors.Join(problems...)
}

func decideOne(ctx context.Context, cfg config, f *Forge, spec JobSpec, current RunRef,
	issues []Issue, currentJobs []Job, jobsErr error) (outcome, error) {
	res := outcome{job: spec}

	verdict, err := VerdictOf(spec.Key, spec.Result)
	if err != nil {
		return res, err
	}
	res.verdict = verdict

	existing, err := findTracker(issues, spec.Key, cfg.ref)
	if err != nil {
		return res, err
	}
	res.action = Decide(verdict, existing != nil)
	if existing != nil {
		res.issue = existing.Index
	}
	if res.action == ActionNone {
		res.note = noteForNone(verdict)
		return res, nil
	}

	history, err := ComputeHistory(ctx, f, cfg.workflow, cfg.ref, cfg.runID, spec.Name, cfg.historyLimit)
	if err != nil {
		return res, err
	}
	res.provenance = ProvenanceOf(history)

	marker := Marker{Job: spec.Key, Ref: cfg.ref, FirstRun: current.ID, FirstAt: current.StartedAt}
	if existing != nil {
		if m, ok := ParseMarker(existing.Body); ok {
			// The first-seen record and the opening classification live nowhere
			// else, so both are carried forward from the body being replaced.
			//
			// PROVENANCE MUST NOT BE RECOMPUTED while the issue is open. It
			// answers "how did this break", and once a break is under way the
			// run before is itself a failure, so a regression re-derives as
			// `resumed` on the very next night — a body contradicting its own
			// title, which is what run 17318 produced before this existed.
			marker = m
			if preserved, ok := ProvenanceNamed(m.Prov); ok {
				res.provenance = preserved
			}
		}
	}
	marker.Prov = res.provenance.String()

	signal := Signal{
		Job:           spec,
		Ref:           cfg.ref,
		DefaultBranch: cfg.defaultBranch,
		Workflow:      cfg.workflow,
		Verdict:       verdict,
		Provenance:    res.provenance,
		History:       history,
		Run:           current,
		Marker:        marker,
	}
	if res.action != ActionClose {
		signal.Excerpt = excerptFor(ctx, f, spec.Name, currentJobs, jobsErr)
	}

	if cfg.mode == modeReport {
		res.note = "report mode: nothing written"
		return res, nil
	}
	return applyAction(ctx, f, res, signal, existing)
}

func applyAction(ctx context.Context, f *Forge, res outcome, signal Signal, existing *Issue) (outcome, error) {
	switch res.action {
	case ActionOpen:
		created, err := f.CreateIssue(ctx, signal.Title(), signal.Body())
		if err != nil {
			return res, err
		}
		res.issue = created.Index
		res.note = "opened #" + strconv.FormatInt(created.Index, 10)
		if err := f.AddLabels(ctx, created.Index, signal.Labels()); err != nil {
			// The signal landed; the triage did not. Reported so the job goes
			// red and someone fixes the label, rather than the mechanism
			// degrading quietly into an issue nobody's filter sees.
			return res, &LabelError{Index: created.Index, Labels: signal.Labels(), Err: err}
		}
		return res, nil

	case ActionUpdate:
		// The TITLE IS NOT REWRITTEN. It records how the job became red, which
		// does not change while it stays red — and a title that churned every
		// night would put the issue back at the top of every activity view,
		// which is the notification this shape exists to avoid.
		if err := f.EditIssue(ctx, existing.Index, existing.Title, signal.Body()); err != nil {
			return res, err
		}
		res.note = "rewrote #" + strconv.FormatInt(existing.Index, 10) + " in place (no comment, no new issue)"
		return res, nil

	case ActionClose:
		if err := f.CommentIssue(ctx, existing.Index, signal.CloseComment()); err != nil {
			return res, err
		}
		if err := f.CloseIssue(ctx, existing.Index); err != nil {
			return res, err
		}
		res.note = "closed #" + strconv.FormatInt(existing.Index, 10)
		return res, nil

	default:
		res.note = noteForNone(res.verdict)
		return res, nil
	}
}

func noteForNone(v Verdict) string {
	switch v {
	case VerdictGreen:
		return "green, and nothing was open"
	case VerdictRed:
		return "red" // unreachable: red always opens or updates.
	default:
		return "neither passed nor failed, so nothing is opened and nothing is closed — " +
			"an inconclusive run proves nothing about the property"
	}
}

// findTracker locates this job's open issue by marker.
func findTracker(issues []Issue, job, ref string) (*Issue, error) {
	prefix := MarkerPrefix(job, ref)
	var found []Issue
	for _, issue := range issues {
		if strings.Contains(issue.Body, prefix) {
			found = append(found, issue)
		}
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return &found[0], nil
	default:
		indexes := make([]int64, 0, len(found))
		for _, issue := range found {
			indexes = append(indexes, issue.Index)
		}
		return nil, &DuplicateIssueError{Marker: prefix, Indexes: indexes}
	}
}

// currentRun describes the run being reported on, falling back to the
// environment when the API cannot be asked.
//
// A FALLBACK RATHER THAN A FAILURE, and this is the one place that trade is
// right: every field it supplies is decoration on the issue body, and refusing
// to signal at all because the run's start time could not be read would lose
// the finding to protect a table cell.
func currentRun(ctx context.Context, cfg config, f *Forge) RunRef {
	ref := RunRef{
		ID:        cfg.runID,
		SHA:       os.Getenv("GITHUB_SHA"),
		Event:     os.Getenv("GITHUB_EVENT_NAME"),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if server := strings.TrimRight(os.Getenv("GITHUB_SERVER_URL"), "/"); server != "" && cfg.runID != 0 {
		ref.URL = server + "/" + cfg.repo + "/actions/runs/" + strconv.FormatInt(cfg.runID, 10)
	}
	if cfg.runID == 0 {
		return ref
	}
	run, err := f.Run(ctx, cfg.runID)
	if err != nil {
		return ref
	}
	if run.StartedAt != "" {
		ref.StartedAt = run.StartedAt
	}
	if run.HeadSHA != "" {
		ref.SHA = run.HeadSHA
	}
	if run.Event != "" {
		ref.Event = run.Event
	}
	if run.HTMLURL != "" {
		ref.URL = run.HTMLURL
	}
	return ref
}

// excerptFor pulls the failing job's log and reduces it to findings.
func excerptFor(ctx context.Context, f *Forge, jobName string, jobs []Job, jobsErr error) Excerpt {
	if jobsErr != nil {
		return Excerpt{Err: jobsErr}
	}
	job, ok := findJob(jobs, jobName)
	if !ok {
		return Excerpt{Err: &ConfigError{
			What: "job " + strconv.Quote(jobName),
			Why:  "does not appear in this run's job listing, so its log could not be located",
		}}
	}
	excerpt := Excerpt{FailedSteps: FailedSteps(job.Steps)}
	log, err := f.JobLog(ctx, job.ID)
	if err != nil {
		excerpt.Err = err
		return excerpt
	}
	excerpt.Lines, excerpt.Matched, excerpt.Scanned = ExtractFindings(log)
	return excerpt
}

// writeSummary prints what was decided, to stdout and to the Actions step
// summary when there is one.
//
// EVERY JOB APPEARS, including the ones nothing was done about. "Trivy: green,
// nothing open" in the log is what lets a reader tell this tool from one that
// silently did nothing — which is the failure mode a notifier cannot otherwise
// be distinguished from.
func writeSummary(out io.Writer, cfg config, current RunRef, outcomes []outcome) {
	var b strings.Builder
	fmt.Fprintf(&b, "### Nightly signal — `%s` on `%s`\n\n", cfg.workflow, cfg.ref)
	fmt.Fprintf(&b, "run %d · mode `%s`\n\n", current.ID, cfg.mode)
	b.WriteString("| job | verdict | provenance | action | note |\n|---|---|---|---|---|\n")
	for _, o := range outcomes {
		provenance := "—"
		if o.action == ActionOpen || o.action == ActionUpdate {
			provenance = o.provenance.String()
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
			o.job.Name, o.verdict, provenance, o.action, o.note)
	}
	fmt.Fprint(out, b.String())

	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return
	}
	// #nosec G304,G703 -- taint source is the runner's own process environment (GITHUB_STEP_SUMMARY), not a request value; append-only sink.
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		fmt.Fprintf(out, "could not append to GITHUB_STEP_SUMMARY: %v\n", err)
		return
	}
	defer func() { _ = file.Close() }()
	fmt.Fprint(file, b.String())
}
