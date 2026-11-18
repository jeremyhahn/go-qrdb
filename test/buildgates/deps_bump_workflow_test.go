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
