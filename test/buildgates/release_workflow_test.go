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

// release.yml validates a release tag AFTER the project agent has already
// pushed it with `make release-alpha` (owner ruling 2026-10-06: "all our
// projects should follow the same standards" as go-quicraft, go-xkms and
// go-quicbaas). There is no release bot and no workflow that creates a tag
// anymore -- this gate keeps it that way, because every property below is
// one edit away from silently not holding: a `push: tags` trigger widened
// "for convenience" so it also fires on a lockstep module's own tag, a
// hand-written `git push --tags`, a RELEASE_BOT secret creeping back in.

import (
	"fmt"
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

// releaseJob is the part of a job this gate rules on.
type releaseJob struct {
	Needs       []string          `yaml:"needs"`
	Permissions map[string]string `yaml:"permissions"`
	Steps       []releaseStep     `yaml:"steps"`
}

// releaseWorkflow is the part of the workflow this gate rules on.
type releaseWorkflow struct {
	On   map[string]yaml.Node  `yaml:"on"`
	Jobs map[string]releaseJob `yaml:"jobs"`
}

// releaseAlphaMention matches a literal invocation of the release recipe.
// It is something an operator's own shell runs now, never a workflow step --
// release.yml included, since it only runs AFTER the tag already exists.
const releaseAlphaMention = "make release-alpha"

// tagWrite matches a shell line that creates or publishes tags by hand: a
// "git tag" command, or a "git push" that targets tags specifically (the
// --tags flag or a refs/tags/ refspec). A push of an ordinary branch ref,
// such as `git push "$GIT_URL" "HEAD:refs/heads/$name"`, does not write a
// tag and must not match -- that is the merge train landing a branch, not
// a release.
var tagWrite = regexp.MustCompile(`(^|[;&|\s])git\s+(-C\s+\S+\s+)?(tag(\s|$)|push\b[^;&|\n]*(--tags\b|refs/tags/))`)

// releaseWorkflowDefects returns every way src fails to be the validation-only
// path the 2026-10-06 ruling describes. An empty result is what the workflow
// is supposed to be.
func releaseWorkflowDefects(src []byte) []string {
	var wf releaseWorkflow
	if err := yaml.Unmarshal(src, &wf); err != nil {
		return []string{"not parseable YAML: " + err.Error()}
	}
	var defects []string

	if strings.Contains(string(src), "RELEASE_BOT") {
		defects = append(defects, "RELEASE_BOT still appears; there is no release bot in this path (release-versioning.md §1a)")
	}

	if len(wf.On) != 1 {
		defects = append(defects, "the only trigger must be the tag push; an extra trigger key fires a release run nobody asked for")
	}
	if pushNode, ok := wf.On["push"]; !ok {
		defects = append(defects, "the trigger must be push: tags: [\"v*\"]")
	} else {
		var push struct {
			Tags []string `yaml:"tags"`
		}
		if err := pushNode.Decode(&push); err != nil {
			defects = append(defects, "on.push is not parseable: "+err.Error())
		} else if len(push.Tags) != 1 || push.Tags[0] != "v*" {
			defects = append(defects, fmt.Sprintf("on.push.tags must be exactly [\"v*\"], got %v; a wider pattern also fires on a lockstep module's own tag", push.Tags))
		}
	}

	for name, job := range wf.Jobs {
		for _, s := range job.Steps {
			if s.ContinueOnError != "" && s.ContinueOnError != "false" {
				defects = append(defects, "job "+name+" step "+s.Name+" has continue-on-error; a release gate's failure must fail the run")
			}
			if strings.Contains(s.Run, releaseAlphaMention) {
				defects = append(defects, "job "+name+" step "+s.Name+" runs "+releaseAlphaMention+"; no workflow may create a release tag")
			}
			for _, line := range strings.Split(s.Run, "\n") {
				if tagWrite.MatchString(line) {
					defects = append(defects, "job "+name+" step "+s.Name+" writes a tag by hand ("+strings.TrimSpace(line)+"); release-alpha, run by the operator, is the only tag writer")
				}
			}
		}
	}

	if validate, ok := wf.Jobs["validate"]; !ok {
		defects = append(defects, "no `validate` job")
	} else {
		all := concatRuns(validate.Steps)
		if !strings.Contains(all, "check-version-reconciled.sh") {
			defects = append(defects, "validate must check VERSION and CHANGELOG.md against the tag with check-version-reconciled.sh")
		}
		if !strings.Contains(all, "check-tag-lockstep.sh") {
			defects = append(defects, "validate must audit the lockstep tags with check-tag-lockstep.sh")
		}
		if !strings.Contains(all, "sleep") {
			defects = append(defects, "validate's lockstep audit must retry; an atomic push does not bound how soon a fetch on another connection sees every sibling tag")
		}
		if !strings.Contains(all, "-alpha") {
			defects = append(defects, "validate must accept the -alpha pre-release suffix this repository's tags use")
		}
	}

	if gate, ok := wf.Jobs["gate"]; !ok {
		defects = append(defects, "no `gate` job")
	} else {
		all := concatRuns(gate.Steps)
		for _, want := range []string{"make fmt", "make vet", "make vet-tags", "make secure-analyzer", "make lint", "go build ./...", "make build-server", "make coverage", "make test-tags"} {
			if !strings.Contains(all, want) {
				defects = append(defects, "gate must run `"+want+"`, the matching ci.yml command")
			}
		}
	}

	if publish, ok := wf.Jobs["publish"]; !ok {
		defects = append(defects, "no `publish` job")
	} else {
		if publish.Permissions["contents"] != "write" {
			defects = append(defects, "publish must set permissions: contents: write to create the release")
		}
		var needsValidate, needsGate bool
		for _, n := range publish.Needs {
			needsValidate = needsValidate || n == "validate"
			needsGate = needsGate || n == "gate"
		}
		if !needsValidate || !needsGate {
			defects = append(defects, "publish must need both validate and gate; it must not publish notes for a tag that failed either")
		}
		all := concatRuns(publish.Steps)
		if !strings.Contains(all, "github.api_url") {
			defects = append(defects, "publish must address the forge's own API through github.api_url, not a hardcoded host")
		}
		if !strings.Contains(all, "CHANGELOG.md") {
			defects = append(defects, "publish must extract the release notes from CHANGELOG.md")
		}
	}

	return defects
}

// concatRuns joins every step's run script, so a substring check does not
// have to care which single step a command landed in.
func concatRuns(steps []releaseStep) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(s.Run)
		b.WriteString("\n")
	}
	return b.String()
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
		"a second trigger": {
			"on:\n  push:\n    tags: [\"v*\"]\n",
			"on:\n  push:\n    tags: [\"v*\"]\n  workflow_dispatch:\n",
			"trigger",
		},
		"a widened tag pattern": {
			"tags: [\"v*\"]",
			"tags: [\"v*\", \"sdk/go/v*\"]",
			"tags",
		},
		"a hand-written tag push": {
			"        run: |\n          set -eu\n          jq -n \\",
			"        run: |\n          set -eu\n          git push origin --tags\n          jq -n \\",
			"by hand",
		},
		"a workflow that cuts its own release": {
			"- name: gofmt\n        run: make fmt",
			"- name: gofmt\n        run: make fmt && make release-alpha",
			"release-alpha",
		},
		"no bounded retry on the lockstep audit": {
			"            sleep 3\n          done",
			"          done",
			"retry",
		},
		"no lockstep audit at all": {
			"./scripts/check-tag-lockstep.sh",
			"true",
			"lockstep",
		},
		"no VERSION/CHANGELOG reconciliation": {
			"./scripts/check-version-reconciled.sh . \"${GITHUB_REF_NAME}\"",
			"true",
			"VERSION",
		},
		"gate drops a command": {
			"run: make coverage",
			"run: echo skip",
			"coverage",
		},
		"publish loses its write permission": {
			"    permissions:\n      contents: write",
			"    permissions:\n      contents: read",
			"contents: write",
		},
		"publish stops needing gate": {
			"needs: [validate, gate]",
			"needs: [validate]",
			"must need",
		},
		"publish hardcodes github.com": {
			"\"${{ github.api_url }}/repos/${{ github.repository }}/releases\"",
			"\"https://api.github.com/repos/${{ github.repository }}/releases\"",
			"github.api_url",
		},
		"the release bot reappears": {
			"FORGE_TOKEN: ${{ secrets.GITHUB_TOKEN }}",
			"FORGE_TOKEN: ${{ secrets.RELEASE_BOT_TOKEN }}",
			"RELEASE_BOT",
		},
		"a release step tolerates failure": {
			"- name: gofmt\n        run: make fmt",
			"- name: gofmt\n        continue-on-error: true\n        run: make fmt",
			"continue-on-error",
		},
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

// tagWriteViolations scans every workflow file under root's workflowDirs,
// release.yml included now that it creates no tag either, and returns one
// message per step whose shell `run` writes a tag or calls release-alpha,
// plus the count of files scanned. Factored out of
// TestNoWorkflowCreatesTags so a fixture tree, not just the real one, can
// drive it: a gate that has only ever run against the tree it already
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
			if e.IsDir() || !(strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml")) {
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
					if strings.Contains(s.Run, releaseAlphaMention) {
						violations = append(violations, e.Name()+" job "+job+" runs "+releaseAlphaMention+"; only the operator's own shell may")
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

// TestNoWorkflowCreatesTags: release-alpha, run by hand, is the only tag
// writer. No workflow -- release.yml included -- may create one or call it.
func TestNoWorkflowCreatesTags(t *testing.T) {
	violations, seen := tagWriteViolations(t, repoRoot(t))
	for _, v := range violations {
		t.Error(v)
	}
	// Discovery floor: ci, nightly, release-lockstep, automerge and release
	// itself. An empty directory walk would pass this test vacuously.
	if seen < 4 {
		t.Fatalf("scanned %d workflow files; expected at least 4", seen)
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
	if err := os.MkdirAll(dir, 0o750); err != nil {
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
	if err := os.WriteFile(filepath.Join(dir, "rogue.yml"), []byte(fixture), 0o600); err != nil {
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
