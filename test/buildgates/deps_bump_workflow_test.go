package buildgates

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// depsBumpWorkflowName is ruling 640's daily dependency bump.
const depsBumpWorkflowName = "deps-bump.yml"

// bareMakeBuild matches `make build` as a target of its own, not the
// build-server or build-cli-headless targets: the plain build target drives
// docker compose through build-client, which the toolchain image lacks.
var bareMakeBuild = regexp.MustCompile(`make build([^-\w]|$)`)

// depsBumpProblems lists where deps-bump.yml departs from what CI accepts:
// the build runs the native targets CI's build job runs in the toolchain
// image, and the bump commit is made by createCommitOnBranch, which GitHub
// signs (CI's Signatures job rejects the unsigned commit POST /git/commits
// makes), against the expected head and checked verified before the PR.
func depsBumpProblems(src string) []string {
	var p []string
	if bareMakeBuild.MatchString(src) {
		p = append(p, "runs `make build`, which needs docker compose (build-client); run make build-server build-cli-headless as CI does")
	}
	if !strings.Contains(src, "make build-server build-cli-headless") {
		p = append(p, "does not run make build-server build-cli-headless")
	}
	if strings.Contains(src, `"repos/$REPO/git/commits"`) || strings.Contains(src, `"repos/$REPO/git/trees"`) {
		p = append(p, "creates the commit through the git data API, which leaves it unsigned")
	}
	for _, want := range []string{"createCommitOnBranch", "expectedHeadOid", ".verification.verified"} {
		if !strings.Contains(src, want) {
			p = append(p, "lacks "+want+": the commit must be made by createCommitOnBranch on the expected head and checked verified")
		}
	}
	return p
}

func TestDepsBumpWorkflowBuildsAndSigns(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", depsBumpWorkflowName))
	if err != nil {
		t.Fatalf("read %s: %v", depsBumpWorkflowName, err)
	}
	if p := depsBumpProblems(string(b)); len(p) != 0 {
		t.Fatalf("%s:\n  %s", depsBumpWorkflowName, strings.Join(p, "\n  "))
	}
}

func TestDepsBumpProblems_CanFail(t *testing.T) {
	good := `scripts/in-toolchain-image.sh "$TOOLCHAIN_IMAGE" 'make deps-bump && make build-server build-cli-headless'
gh api graphql -f query='mutation($in: CreateCommitOnBranchInput!) { createCommitOnBranch(input: $in) { commit { oid } } }' # expectedHeadOid
gh api "repos/$REPO/commits/$c" --jq .commit.verification.verified
`
	if p := depsBumpProblems(good); len(p) != 0 {
		t.Fatalf("good workflow flagged: %v", p)
	}
	for name, tc := range map[string]struct{ from, to, want string }{
		"bare make build":      {"make build-server build-cli-headless", "make build", "make build"},
		"make build then more": {"make build-server build-cli-headless'", "make build; true'", "make build"},
		"unsigned commit":      {"# expectedHeadOid", `# expectedHeadOid` + "\n" + `gh api "repos/$REPO/git/commits" --input -`, "git data API"},
		"no expected head":     {"# expectedHeadOid", "", "expectedHeadOid"},
		"no verified check":    {".commit.verification.verified", ".sha", "verification.verified"},
	} {
		t.Run(name, func(t *testing.T) {
			p := depsBumpProblems(strings.Replace(good, tc.from, tc.to, 1))
			if !strings.Contains(strings.Join(p, "\n"), tc.want) {
				t.Fatalf("want a problem naming %q, got %v", tc.want, p)
			}
		})
	}
}

// depsBumpPRStep is the name of deps-bump.yml's PR-opening step.
const depsBumpPRStep = "- name: Open the PR and label it"

// depsBumpRecoveryProblems lists where deps-bump.yml can strand
// ci/deps-bump: a failed PR create leaves a branch with no open PR, which
// the base step refuses on every later run. The commit step records a branch
// it created in GITHUB_ENV; the PR step catches a failed create, looks up an
// open PR for the branch, and only when there is none and this run created
// the branch deletes it. No step force-updates a ref.
func depsBumpRecoveryProblems(src string) []string {
	var p []string
	if !strings.Contains(src, `echo "BUMP_BRANCH_CREATED=1" >> "$GITHUB_ENV"`) {
		p = append(p, "the commit step does not write BUMP_BRANCH_CREATED=1 to GITHUB_ENV when it creates the branch")
	}
	if strings.Contains(src, "force=true") || strings.Contains(src, "push --force") || strings.Contains(src, "push -f") {
		p = append(p, "force-updates a ref")
	}
	i := strings.Index(src, depsBumpPRStep)
	if i < 0 {
		return append(p, "has no step "+depsBumpPRStep)
	}
	step := src[i+len(depsBumpPRStep):]
	if j := strings.Index(step, "\n      - name:"); j >= 0 {
		step = step[:j]
	}
	order := []struct{ text, what string }{
		{`if PR=$(`, "catches a failed PR create (if PR=$(...))"},
		{`"repos/$REPO/pulls?state=open&head=$owner:$BRANCH`, "looks up an open PR for the branch after a failed create"},
		{`"${BUMP_BRANCH_CREATED:-}" = 1`, "guards the delete with BUMP_BRANCH_CREATED"},
		{`gh api -X DELETE "repos/$REPO/git/refs/heads/$BRANCH"`, "deletes the branch it created"},
	}
	at := 0
	for _, o := range order {
		k := strings.Index(step[at:], o.text)
		if k < 0 {
			p = append(p, "the PR step never, in order, "+o.what)
			return p
		}
		at += k + len(o.text)
	}
	del := order[len(order)-1].text
	if strings.Index(step, del) < strings.Index(step, order[2].text) {
		p = append(p, "the PR step deletes the branch before the open-PR lookup and the BUMP_BRANCH_CREATED guard")
	}
	return p
}

func TestDepsBumpWorkflowRecoversFailedPRCreate(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", depsBumpWorkflowName))
	if err != nil {
		t.Fatalf("read %s: %v", depsBumpWorkflowName, err)
	}
	if p := depsBumpRecoveryProblems(string(b)); len(p) != 0 {
		t.Fatalf("%s:\n  %s", depsBumpWorkflowName, strings.Join(p, "\n  "))
	}
}

func TestDepsBumpRecoveryProblems_CanFail(t *testing.T) {
	good := `            created=1
            echo "BUMP_BRANCH_CREATED=1" >> "$GITHUB_ENV"
      - name: Open the PR and label it
        run: |
            if PR=$(jq -n '{}' | gh api "repos/$REPO/pulls" --input - --jq .number); then
              echo "opened #$PR"
            else
              PR=$(gh api "repos/$REPO/pulls?state=open&head=$owner:$BRANCH&base=main" --jq '.[0].number // empty' || true)
              if [ -z "$PR" ]; then
                if [ "${BUMP_BRANCH_CREATED:-}" = 1 ]; then
                  gh api -X DELETE "repos/$REPO/git/refs/heads/$BRANCH" >/dev/null || true
                fi
                exit 1
              fi
            fi
      - name: Next
`
	if p := depsBumpRecoveryProblems(good); len(p) != 0 {
		t.Fatalf("good workflow flagged: %v", p)
	}
	for name, tc := range map[string]struct{ from, to, want string }{
		"no env record":    {`echo "BUMP_BRANCH_CREATED=1" >> "$GITHUB_ENV"`, "", "GITHUB_ENV"},
		"create exits":     {"if PR=$(", "PR=$(", "catches a failed PR create"},
		"no lookup":        {"pulls?state=open&head=$owner:$BRANCH", "pulls", "looks up an open PR"},
		"unguarded delete": {`if [ "${BUMP_BRANCH_CREATED:-}" = 1 ]; then`, "if true; then", "BUMP_BRANCH_CREATED"},
		"delete before lookup": {`              PR=$(gh api "repos/$REPO/pulls?state=open`,
			`              gh api -X DELETE "repos/$REPO/git/refs/heads/$BRANCH"` + "\n" + `              PR=$(gh api "repos/$REPO/pulls?state=open`, "deletes the branch before"},
		"force push": {"exit 1", "git push --force origin HEAD; exit 1", "force-updates"},
	} {
		t.Run(name, func(t *testing.T) {
			p := depsBumpRecoveryProblems(strings.Replace(good, tc.from, tc.to, 1))
			if len(p) == 0 || !strings.Contains(strings.Join(p, "\n"), tc.want) {
				t.Fatalf("want a problem naming %q, got %v", tc.want, p)
			}
		})
	}
}

// depsBumpBaseStep is the name of deps-bump.yml's base-commit step.
const depsBumpBaseStep = "- name: Find the base commit"

// depsBumpBranchDeleteProblems lists where deps-bump.yml's base step can
// delete ci/deps-bump with unmerged commits on it: the delete must follow a
// read of the branch tip, the last merged PR's head and the compare status,
// and a guard that refuses (exit 1) unless the head is the tip or the tip
// is in main (behind or identical).
func depsBumpBranchDeleteProblems(src string) []string {
	i := strings.Index(src, depsBumpBaseStep)
	if i < 0 {
		return []string{"has no step " + depsBumpBaseStep}
	}
	step := src[i+len(depsBumpBaseStep):]
	if j := strings.Index(step, "\n      - "); j >= 0 {
		step = step[:j]
	}
	del := `gh api -X DELETE "repos/$REPO/git/refs/heads/$BRANCH"`
	d := strings.Index(step, del)
	if d < 0 {
		return nil
	}
	before := step[:d]
	var p []string
	for _, c := range []struct{ text, what string }{
		{`tip=$(gh api "repos/$REPO/git/ref/heads/$BRANCH" --jq .object.sha`, "reads the branch tip SHA"},
		{`select(.merged_at != null)][0].head.sha`, "reads the last merged PR's head.sha"},
		{`"repos/$REPO/compare/main...$tip"`, "asks the compare API whether the tip is in main"},
		{`[ "$merged_head" != "$tip" ] && [ "$status" != behind ] && [ "$status" != identical ]`, "refuses unless the merged head is the tip or the tip is behind/identical to main"},
	} {
		if !strings.Contains(before, c.text) {
			p = append(p, "the base step deletes the branch but never, before it, "+c.what)
		}
	}
	g := strings.LastIndex(before, `[ "$merged_head" != "$tip" ]`)
	if g >= 0 && !strings.Contains(before[g:], "exit 1") {
		p = append(p, "the base step's merged-tip guard does not exit before the delete")
	}
	return p
}

func TestDepsBumpWorkflowDeletesOnlyAMergedTip(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", depsBumpWorkflowName))
	if err != nil {
		t.Fatalf("read %s: %v", depsBumpWorkflowName, err)
	}
	if p := depsBumpBranchDeleteProblems(string(b)); len(p) != 0 {
		t.Fatalf("%s:\n  %s", depsBumpWorkflowName, strings.Join(p, "\n  "))
	}
}

func TestDepsBumpBranchDeleteProblems_CanFail(t *testing.T) {
	good := `      - name: Find the base commit
        run: |
            if tip=$(gh api "repos/$REPO/git/ref/heads/$BRANCH" --jq .object.sha 2>/dev/null); then
              merged_head=$(gh api "repos/$REPO/pulls?state=closed" --jq '[.[] | select(.merged_at != null)][0].head.sha // empty')
              status=$(gh api "repos/$REPO/compare/main...$tip" --jq .status 2>/dev/null || true)
              if [ "$merged_head" != "$tip" ] && [ "$status" != behind ] && [ "$status" != identical ]; then
                echo "::error::refusing"
                exit 1
              fi
              gh api -X DELETE "repos/$REPO/git/refs/heads/$BRANCH" >/dev/null
            fi
      - name: Next
`
	if p := depsBumpBranchDeleteProblems(good); len(p) != 0 {
		t.Fatalf("good workflow flagged: %v", p)
	}
	for name, tc := range map[string]struct{ from, to, want string }{
		"no tip read":      {`tip=$(gh api "repos/$REPO/git/ref/heads/$BRANCH" --jq .object.sha`, `gh api "repos/$REPO/git/ref/heads/$BRANCH"`, "branch tip SHA"},
		"merged_at only":   {`[.[] | select(.merged_at != null)][0].head.sha // empty`, `.[0].merged_at // empty`, "head.sha"},
		"no compare":       {`"repos/$REPO/compare/main...$tip"`, `"repos/$REPO/branches/main"`, "compare API"},
		"no tip guard":     {`[ "$merged_head" != "$tip" ] && `, `[ -z "$merged_head" ] && `, "refuses unless"},
		"guard falls thru": {"exit 1", "true", "does not exit"},
	} {
		t.Run(name, func(t *testing.T) {
			p := depsBumpBranchDeleteProblems(strings.Replace(good, tc.from, tc.to, 1))
			if len(p) == 0 || !strings.Contains(strings.Join(p, "\n"), tc.want) {
				t.Fatalf("want a problem naming %q, got %v", tc.want, p)
			}
		})
	}
}
