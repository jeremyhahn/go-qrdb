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
	ContinueOnError string            `yaml:"continue-on-error"`
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

// tagWrite matches a shell line that creates or publishes tags by hand.
var tagWrite = regexp.MustCompile(`(^|[;&|\s])git\s+(-C\s+\S+\s+)?(tag|push)(\s|$)`)

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
	for i, s := range job.Steps {
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
			if s.With["token"] != "${{ secrets.RELEASE_BOT_TOKEN }}" {
				defects = append(defects, "checkout must authenticate as the release bot (secrets.RELEASE_BOT_TOKEN); tag protection admits only that principal")
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
		case strings.Contains(s.Run, "make check-tag-lockstep"):
			idx["audit"] = i + 1
			if !strings.Contains(s.Run, "git fetch --quiet --tags origin") {
				defects = append(defects, "the post-push lockstep audit must fetch the remote's tags first, or it audits only what this clone sent")
			}
		}
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
		"the runner's own token":  {"token: ${{ secrets.RELEASE_BOT_TOKEN }}", "token: ${{ github.token }}", "release bot"},
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

// TestNoOtherWorkflowCreatesTags: the Release workflow being correct is only
// half of "the only path"; a second workflow that tags would be a second path.
func TestNoOtherWorkflowCreatesTags(t *testing.T) {
	root := repoRoot(t)
	seen := 0
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
						t.Errorf("%s job %s runs make release-alpha; the Release workflow is the only release path", e.Name(), job)
					}
					for _, line := range strings.Split(s.Run, "\n") {
						if tagWrite.MatchString(line) {
							t.Errorf("%s job %s step %q writes tags: %s", e.Name(), job, s.Name, strings.TrimSpace(line))
						}
					}
				}
			}
		}
	}
	// Discovery floor: ci, nightly and release-lockstep at least. An empty
	// directory walk would pass this test vacuously.
	if seen < 3 {
		t.Fatalf("scanned %d other workflow files; expected at least 3", seen)
	}
}
