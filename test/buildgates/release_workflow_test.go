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

// The Release workflow is the only path that creates a release tag (#13, owner
// ruling 2026-09-30: "workflow wraps release-alpha"). Tag protection makes it
// the only path on the server; this gate keeps the workflow itself the path
// the ruling describes, because every property below is one edit away from
// silently not holding -- a `push: tags` trigger added "for convenience", a
// hand-written `git push`, a VERSION typed into a dispatch input.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// releaseStep is the part of a step this gate rules on.
type releaseStep struct {
	Name            string            `yaml:"name"`
	Uses            string            `yaml:"uses"`
	Run             string            `yaml:"run"`
	With            map[string]string `yaml:"with"`
	Env             map[string]string `yaml:"env"`
	ContinueOnError string            `yaml:"continue-on-error"`
}

// botToken is the expression that names the release bot's token. It may
// appear in exactly one place: the env of the step that pushes.
const botToken = "secrets.RELEASE_BOT_TOKEN"

// mentions reports whether any value of m contains needle.
func mentions(m map[string]string, needle string) bool {
	for _, v := range m {
		if strings.Contains(v, needle) {
			return true
		}
	}
	return false
}

// releaseWorkflow is the part of the workflow this gate rules on.
type releaseWorkflow struct {
	On          map[string]yaml.Node `yaml:"on"`
	Concurrency struct {
		CancelInProgress *bool `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Jobs map[string]struct {
		If    string        `yaml:"if"`
		Steps []releaseStep `yaml:"steps"`
	} `yaml:"jobs"`
}

// tagWrite matches a shell line that creates or publishes tags by hand: a
// "git tag" command, or a "git push" that targets tags specifically (the
// --tags flag or a refs/tags/ refspec). A push of an ordinary branch ref,
// such as `git push "$GIT_URL" "HEAD:refs/heads/$name"`, does not write a
// tag and must not match -- that is the merge train landing a branch, not
// a release.
var tagWrite = regexp.MustCompile(`(^|[;&|\s])git\s+(-C\s+\S+\s+)?(tag(\s|$)|push\b[^;&|\n]*(--tags\b|refs/tags/))`)

// releaseWorkflowDefects returns every way src fails to be the release path
// the ruling describes. An empty result is the workflow #13 asks for.
func releaseWorkflowDefects(src []byte) []string {
	var wf releaseWorkflow
	if err := yaml.Unmarshal(src, &wf); err != nil {
		return []string{"not parseable YAML: " + err.Error()}
	}
	var defects []string

	if _, ok := wf.On["workflow_dispatch"]; !ok || len(wf.On) != 1 {
		defects = append(defects, "the only trigger must be workflow_dispatch; a push or schedule trigger cuts releases nobody asked for")
	}
	if wf.Concurrency.CancelInProgress == nil || *wf.Concurrency.CancelInProgress {
		defects = append(defects, "concurrency must set cancel-in-progress: false; a cancelled run can stop between tagging and pushing")
	}
	job, ok := wf.Jobs["release"]
	if !ok {
		return append(defects, "no `release` job")
	}
	if !strings.Contains(job.If, "github.server_url != 'https://github.com'") {
		defects = append(defects, "the release job must not run on github.com; publication there is a separate, approved step")
	}

	idx := map[string]int{}
	tokenSteps := 0
	for i, s := range job.Steps {
		if mentions(s.With, botToken) || mentions(s.Env, botToken) || strings.Contains(s.Run, botToken) {
			tokenSteps++
			if !strings.Contains(s.Run, "make release-alpha") || !mentions(s.Env, botToken) {
				defects = append(defects, "step "+s.Name+" receives the release bot's token; only the make release-alpha step may, and only through its env")
			}
		}
		if s.ContinueOnError != "" && s.ContinueOnError != "false" {
			defects = append(defects, "step "+s.Name+" has continue-on-error; a release step's failure must fail the release")
		}
		for _, line := range strings.Split(s.Run, "\n") {
			if tagWrite.MatchString(line) {
				defects = append(defects, "step "+s.Name+" creates or pushes tags by hand ("+strings.TrimSpace(line)+"); release-alpha is the only tag writer")
			}
		}
		switch {
		case strings.HasPrefix(s.Uses, "actions/checkout@"):
			idx["checkout"] = i + 1
			if s.With["persist-credentials"] != "false" {
				defects = append(defects, "checkout must set persist-credentials: false; a persisted token in .git/config is readable by every later step")
			}
			if s.With["ref"] != "main" || s.With["fetch-depth"] != "0" {
				defects = append(defects, "checkout must take main with full history; the lockstep audit needs every tag")
			}
		case strings.Contains(s.Run, "< VERSION"):
			idx["version"] = i + 1
		case strings.Contains(s.Run, "make release-alpha"):
			idx["release"] = i + 1
			if !strings.Contains(s.Run, `VERSION="$VERSION_TAG"`) {
				defects = append(defects, "make release-alpha must be given the version read from VERSION, which is authoritative")
			}
			if !strings.Contains(s.Run, "scripts/release-bot-git.sh") {
				defects = append(defects, "make release-alpha must sign through scripts/release-bot-git.sh")
			}
			if !mentions(s.Env, botToken) || !strings.Contains(s.Run, ". ./scripts/git-extraheader.sh") {
				defects = append(defects, "make release-alpha must receive the release bot's token in its env and turn it into a per-step header with scripts/git-extraheader.sh; tag protection admits only that principal")
			}
		case strings.Contains(s.Run, "make check-tag-lockstep"):
			idx["audit"] = i + 1
			if !strings.Contains(s.Run, "git fetch --quiet --tags origin") {
				defects = append(defects, "the post-push lockstep audit must fetch the remote's tags first, or it audits only what this clone sent")
			}
		}
	}
	if tokenSteps > 1 {
		defects = append(defects, "the release bot's token reaches more than one step")
	}
	for _, want := range []string{"checkout", "version", "release", "audit"} {
		if idx[want] == 0 {
			defects = append(defects, "missing the "+want+" step")
		}
	}
	if idx["version"] != 0 && idx["release"] != 0 && idx["version"] > idx["release"] {
		defects = append(defects, "VERSION must be read before release-alpha runs")
	}
	if idx["release"] != 0 && idx["audit"] != 0 && idx["audit"] < idx["release"] {
		defects = append(defects, "the lockstep audit of the published tags must run after the push")
	}
	return defects
}

func readReleaseWorkflow(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read the Release workflow: %v", err)
	}
	return raw
}

// TestTheReleaseWorkflowIsTheRulingsReleasePath is the gate on the real file.
func TestTheReleaseWorkflowIsTheRulingsReleasePath(t *testing.T) {
	if d := releaseWorkflowDefects(readReleaseWorkflow(t)); len(d) != 0 {
		t.Fatalf("release.yml:\n  %s", strings.Join(d, "\n  "))
	}
}

// TestTheReleaseGateSeesEachWayTheWorkflowCanDrift is the negative control: a
// gate that cannot go red proves nothing, so each mutation of the real file
// must produce the defect it names.
func TestTheReleaseGateSeesEachWayTheWorkflowCanDrift(t *testing.T) {
	real := string(readReleaseWorkflow(t))
	for name, tc := range map[string]struct{ from, to, want string }{
		"a tag-push trigger":      {"on:\n  workflow_dispatch:\n", "on:\n  workflow_dispatch:\n  push:\n    tags: [\"v*\"]\n", "only trigger"},
		"cancellable":             {"cancel-in-progress: false", "cancel-in-progress: true", "cancel-in-progress"},
		"runs on github.com":      {"if: github.server_url != 'https://github.com'", "if: always()", "github.com"},
		"a persisted credential":  {"persist-credentials: false", "persist-credentials: true", "persist-credentials"},
		"no persist setting":      {"          persist-credentials: false\n", "", "persist-credentials"},
		"the token at checkout":   {"          persist-credentials: false\n", "          persist-credentials: false\n          token: ${{ secrets.RELEASE_BOT_TOKEN }}\n", "only the make release-alpha step"},
		"the runner's own token":  {"FORGE_TOKEN: ${{ secrets.RELEASE_BOT_TOKEN }}", "FORGE_TOKEN: ${{ github.token }}", "release bot's token in its env"},
		"no per-step header":      {"          . ./scripts/git-extraheader.sh\n          export RELEASE_BOT_SIGNING_KEY_FILE", "          export RELEASE_BOT_SIGNING_KEY_FILE", "per-step header"},
		"a hand-written tag push": {"make check-tag-lockstep", "git push origin --tags && make check-tag-lockstep", "by hand"},
		"a typed version":         {`VERSION="$VERSION_TAG"`, `VERSION="v9.9.9-alpha"`, "authoritative"},
		"no post-push audit":      {"make check-tag-lockstep", "echo skipped", "audit step"},
		"an audit of local tags":  {"git fetch --quiet --tags origin", "true", "fetch the remote"},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(real, tc.from) {
				t.Fatalf("the real workflow no longer contains %q; update this mutation", tc.from)
			}
			d := strings.Join(releaseWorkflowDefects([]byte(strings.Replace(real, tc.from, tc.to, 1))), "\n")
			if !strings.Contains(d, tc.want) {
				t.Fatalf("mutation %q not caught; defects:\n%s", name, d)
			}
		})
	}
}

// tagWriteViolations scans every non-release workflow file under root's
// workflowDirs and returns one message per step whose shell `run` writes a
// tag, plus the count of files scanned. Factored out of
// TestNoOtherWorkflowCreatesTags so a fixture tree, not just the real one,
// can drive it: a gate that has only ever run against the tree it already
// passes has never been shown that it can fail.
func tagWriteViolations(t *testing.T, root string) (violations []string, seen int) {
	t.Helper()
	for _, dir := range workflowDirs {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || e.Name() == "release.yml" || !(strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml")) {
				continue
			}
			seen++
			raw, err := os.ReadFile(filepath.Join(root, dir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			var doc struct {
				Jobs map[string]struct {
					Steps []releaseStep `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("%s is not parseable YAML: %v", e.Name(), err)
			}
			for job, j := range doc.Jobs {
				for _, s := range j.Steps {
					if strings.Contains(s.Run, "make release-alpha") {
						violations = append(violations, e.Name()+" job "+job+" runs make release-alpha; the Release workflow is the only release path")
					}
					for _, line := range strings.Split(s.Run, "\n") {
						if tagWrite.MatchString(line) {
							violations = append(violations, e.Name()+" job "+job+" step "+s.Name+" writes tags: "+strings.TrimSpace(line))
						}
					}
				}
			}
		}
	}
	return violations, seen
}

// TestNoOtherWorkflowCreatesTags: the Release workflow being correct is only
// half of "the only path"; a second workflow that tags would be a second path.
func TestNoOtherWorkflowCreatesTags(t *testing.T) {
	violations, seen := tagWriteViolations(t, repoRoot(t))
	for _, v := range violations {
		t.Error(v)
	}
	// Discovery floor: ci, nightly and release-lockstep at least. An empty
	// directory walk would pass this test vacuously.
	if seen < 3 {
		t.Fatalf("scanned %d other workflow files; expected at least 3", seen)
	}
}

// TestTagWriteDetectionCatchesARealTagPush is the negative control the gate
// above needs: a fixture workflow that genuinely tags and pushes tags by
// hand must still be flagged, proving tagWrite can go red at all rather than
// having been narrowed into never firing. It also plants a plain branch
// push shaped exactly like automerge.yml's merge-train landing step, to
// prove the narrowing did not go blind in the other direction.
func TestTagWriteDetectionCatchesARealTagPush(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const fixture = `on:
  push:
jobs:
  tag:
    steps:
      - name: cut a release tag by hand
        run: |
          git tag v9.9.9
          git push origin --tags
      - name: land a merge-train branch
        run: |
          git -C "$repo" push -q "$GIT_URL" "HEAD:refs/heads/merge-train/x" || die "cannot push"
`
	if err := os.WriteFile(filepath.Join(dir, "rogue.yml"), []byte(fixture), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	violations, _ := tagWriteViolations(t, root)
	if len(violations) != 2 {
		t.Fatalf("want exactly 2 tag-write violations (git tag, git push --tags), got %d: %v", len(violations), violations)
	}
	for _, v := range violations {
		if strings.Contains(v, "refs/heads") {
			t.Fatalf("a plain branch push was flagged as a tag write: %s", v)
		}
	}
}
