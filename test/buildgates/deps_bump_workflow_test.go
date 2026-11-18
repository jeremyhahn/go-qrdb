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
// image.
func depsBumpProblems(src string) []string {
	var p []string
	if bareMakeBuild.MatchString(src) {
		p = append(p, "runs `make build`, which needs docker compose (build-client); run make build-server build-cli-headless as CI does")
	}
	if !strings.Contains(src, "make build-server build-cli-headless") {
		p = append(p, "does not run make build-server build-cli-headless")
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
`
	if p := depsBumpProblems(good); len(p) != 0 {
		t.Fatalf("good workflow flagged: %v", p)
	}
	for name, tc := range map[string]struct{ from, to, want string }{
		"bare make build":      {"make build-server build-cli-headless", "make build", "make build"},
		"make build then more": {"make build-server build-cli-headless'", "make build; true'", "make build"},
	} {
		t.Run(name, func(t *testing.T) {
			p := depsBumpProblems(strings.Replace(good, tc.from, tc.to, 1))
			if !strings.Contains(strings.Join(p, "\n"), tc.want) {
				t.Fatalf("want a problem naming %q, got %v", tc.want, p)
			}
		})
	}
}
