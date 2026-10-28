package buildgates

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// draftSkipCIPath is the workflow whose draft skip ruling 214 requires.
var draftSkipCIPath = filepath.Join("..", "..", ".github", "workflows", "ci.yml")

// draftSkipGateJob is the one job that runs on a draft, to fail it.
const draftSkipGateJob = "ci-gate"

// draftSkipCondition is the clause every other job's if: carries.
const draftSkipCondition = "!github.event.pull_request.draft"

type draftSkipWorkflow struct {
	On struct {
		PullRequest struct {
			Types []string `yaml:"types"`
		} `yaml:"pull_request"`
	} `yaml:"on"`
	Jobs map[string]struct {
		If    string `yaml:"if"`
		Steps []struct {
			Env map[string]any `yaml:"env"`
			Run string         `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadDraftSkipWorkflow(t *testing.T) draftSkipWorkflow {
	t.Helper()
	raw, err := os.ReadFile(draftSkipCIPath)
	if err != nil {
		t.Fatalf("read %s: %v", draftSkipCIPath, err)
	}
	var wf draftSkipWorkflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse %s: %v", draftSkipCIPath, err)
	}
	// Discovery floor: every assertion below is inside a loop.
	if len(wf.Jobs) < 10 {
		t.Fatalf("%s: found %d jobs, want at least 10", draftSkipCIPath, len(wf.Jobs))
	}
	return wf
}

func TestCIDraftSkip_EveryJobButGateSkipsOnDraft(t *testing.T) {
	for name, job := range loadDraftSkipWorkflow(t).Jobs {
		if name == draftSkipGateJob {
			continue
		}
		if !strings.Contains(job.If, draftSkipCondition) {
			t.Errorf("job %s: if: %q lacks %q (ruling 214)", name, job.If, draftSkipCondition)
		}
	}
}

func TestCIDraftSkip_GateRunsAndFailsOnDraft(t *testing.T) {
	gate, ok := loadDraftSkipWorkflow(t).Jobs[draftSkipGateJob]
	if !ok {
		t.Fatalf("no %s job", draftSkipGateJob)
	}
	if strings.Contains(gate.If, draftSkipCondition) {
		t.Errorf("%s skips on a draft; a skipped gate is not a failed one", draftSkipGateJob)
	}
	for _, step := range gate.Steps {
		env, _ := step.Env["DRAFT"].(string)
		if strings.Contains(env, "github.event.pull_request.draft == true") &&
			strings.Contains(step.Run, `"$DRAFT" = "true"`) && strings.Contains(step.Run, "exit 1") {
			return
		}
	}
	t.Errorf("%s has no step that fails when DRAFT is true", draftSkipGateJob)
}

func TestCIDraftSkip_NoDraftLiftTrigger(t *testing.T) {
	types := loadDraftSkipWorkflow(t).On.PullRequest.Types
	if !slices.Contains(types, "synchronize") {
		t.Fatalf("pull_request types %v lack synchronize; the floor under this check", types)
	}
	for _, banned := range []string{"edited", "ready_for_review"} {
		if slices.Contains(types, banned) {
			t.Errorf("pull_request types %v include %s; a draft lift starts its run by a push (ruling 214)", types, banned)
		}
	}
}

// withoutDraftSkip returns the condition a job's if: carries besides the
// ruling 214 draft skip, unwrapped from ${{ }}, so the tier checks compare
// the tier condition alone.
func withoutDraftSkip(cond string) string {
	c := strings.TrimSpace(cond)
	if c == "${{ "+draftSkipCondition+" }}" {
		return ""
	}
	if s, ok := strings.CutPrefix(c, "${{ ("); ok {
		if s, ok := strings.CutSuffix(s, ") && "+draftSkipCondition+" }}"); ok {
			return s
		}
	}
	if s, ok := strings.CutPrefix(c, "${{ "); ok {
		if s, ok := strings.CutSuffix(s, " }}"); ok {
			return s
		}
	}
	return c
}

func TestWithoutDraftSkip(t *testing.T) {
	for in, want := range map[string]string{
		"${{ " + draftSkipCondition + " }}":                         "",
		"${{ (" + fullTierIf + ") && " + draftSkipCondition + " }}": fullTierIf,
		fullTierIf:                  fullTierIf,
		"${{ " + fullTierIf + " }}": fullTierIf,
		"":                          "",
	} {
		if got := withoutDraftSkip(in); got != want {
			t.Errorf("withoutDraftSkip(%q) = %q, want %q", in, got, want)
		}
	}
}
