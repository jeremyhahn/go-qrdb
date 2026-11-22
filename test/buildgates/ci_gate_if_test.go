package buildgates

import (
	"strings"
	"testing"
)

// ciGateIf is CI Gate's job-level if: (ci-template v1.53, ruling 1087).
// On a pull request event it is always(): a cancelled PR run still posts a
// failing gate, because a skipped required check counts as passed. Off
// pull_request it is !cancelled(): a main run a newer push cancelled posts
// no gate result, and a run that is not cancelled still reaches the gate
// when a need failed or skipped, so the gate fails closed (Codex on #1680).
const ciGateIf = "${{ startsWith(github.event_name, 'pull_request') && always() || !cancelled() }}"

// ciGateIfProblem returns "" when cond, whitespace collapsed, is ciGateIf,
// and otherwise what is wrong with it.
func ciGateIfProblem(cond string) string {
	if strings.Join(strings.Fields(cond), " ") == ciGateIf {
		return ""
	}
	return "ci-gate's if: is " + cond + ", not " + ciGateIf +
		": a pull request must reach the gate on always() and every other event on !cancelled()"
}

func TestCIGateIf(t *testing.T) {
	wf := parseWorkflow(t, readCIBody(t))
	gate, ok := wf.Jobs[ciGateJob]
	if !ok {
		t.Fatalf("ci.yml has no %s job", ciGateJob)
	}
	if p := ciGateIfProblem(gate.If); p != "" {
		t.Error(p)
	}
}

// TestCIGateIfCanFail: each mutation changes when the required check runs,
// and the checker must refuse it.
func TestCIGateIfCanFail(t *testing.T) {
	for name, cond := range map[string]string{
		"!cancelled() alone (a cancelled PR run skips the required check)": "${{ !cancelled() }}",
		"success() (a failed need skips the gate)":                         "${{ success() }}",
		"always() alone (a superseded main run posts a red gate)":          "${{ always() }}",
		"the arms swapped":                          "${{ startsWith(github.event_name, 'pull_request') && !cancelled() || always() }}",
		"no condition (success() by default)":       "",
		"pull_request only, no pull_request_target": "${{ github.event_name == 'pull_request' && always() || !cancelled() }}",
	} {
		if ciGateIfProblem(cond) == "" {
			t.Errorf("%s: %q passed", name, cond)
		}
	}
	if p := ciGateIfProblem("${{  startsWith(github.event_name, 'pull_request')\n  && always() || !cancelled() }}"); p != "" {
		t.Errorf("a reflowed copy of the expression failed: %s", p)
	}
}
