package buildgates

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// patchItemRE matches a numbered patch item in the README's "## Patches"
// section, for example "1. **quic-go v0.65.0 `HTTPStream`**".
var patchItemRE = regexp.MustCompile(`^(\d+)\. \*\*`)

// webTransportCopyReadmeProblems checks the patched webtransport-go copy's
// README: it must have a "## Dropping this copy" section, and every numbered
// patch under "## Patches" must carry an "Upstream:" line before the next
// patch or section. It returns one message per problem.
func webTransportCopyReadmeProblems(readme string) []string {
	var problems []string
	lines := strings.Split(readme, "\n")

	hasDrop := false
	inPatches := false
	sawPatches := false
	current := ""
	currentHasUpstream := false
	closeItem := func() {
		if current != "" && !currentHasUpstream {
			problems = append(problems, fmt.Sprintf("patch %s has no Upstream: line", current))
		}
		current = ""
		currentHasUpstream = false
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			heading := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			if inPatches {
				closeItem()
			}
			inPatches = heading == "Patches"
			if inPatches {
				sawPatches = true
			}
			if heading == "Dropping this copy" {
				hasDrop = true
			}
			continue
		}
		if !inPatches {
			continue
		}
		if m := patchItemRE.FindStringSubmatch(line); m != nil {
			closeItem()
			current = m[1]
			continue
		}
		if current != "" && strings.HasPrefix(strings.TrimSpace(line), "Upstream:") {
			currentHasUpstream = true
		}
	}
	if inPatches {
		closeItem()
	}

	if !sawPatches {
		problems = append(problems, `no "## Patches" section`)
	}
	if !hasDrop {
		problems = append(problems, `no "## Dropping this copy" section`)
	}
	return problems
}

// TestWebTransportCopy_ReadmeTracksUpstream holds internal/webtransport's
// README to an upstream status line per patch and a documented step to drop
// the copy once upstream ships the fixes.
func TestWebTransportCopy_ReadmeTracksUpstream(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal", "webtransport", "README.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	readme := string(data)
	if !patchItemRE.MatchString(firstPatchLine(readme)) {
		t.Fatalf("%s lists no numbered patch under ## Patches", path)
	}
	for _, p := range webTransportCopyReadmeProblems(readme) {
		t.Errorf("%s: %s", path, p)
	}
}

// firstPatchLine returns the first numbered patch line of the README, or "".
func firstPatchLine(readme string) string {
	for _, line := range strings.Split(readme, "\n") {
		if patchItemRE.MatchString(line) {
			return line
		}
	}
	return ""
}

// TestWebTransportCopy_ReadmeTracksUpstream_Detects proves the check reports
// the patch that lacks an Upstream: line and a missing drop section.
func TestWebTransportCopy_ReadmeTracksUpstream_Detects(t *testing.T) {
	readme := strings.Join([]string{
		"# copy",
		"",
		"## Patches",
		"",
		"1. **First** (`a.go`). Text.",
		"   Upstream: none filed (2026-11-23).",
		"2. **Second** (`b.go`). Text",
		"   continues.",
		"3. **Third** (`c.go`). Text.",
		"   Upstream: none filed (2026-11-23).",
		"",
		"## Tests",
		"",
		"Upstream: a stray line outside Patches counts for nothing.",
		"",
	}, "\n")

	got := webTransportCopyReadmeProblems(readme)
	want := []string{
		"patch 2 has no Upstream: line",
		`no "## Dropping this copy" section`,
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("problems = %q, want %q", got, want)
	}

	fixed := strings.Replace(readme, "   continues.", "   continues.\n   Upstream: issue #1 open.", 1) +
		"## Dropping this copy\n\nSteps.\n"
	if got := webTransportCopyReadmeProblems(fixed); len(got) != 0 {
		t.Fatalf("problems on a complete README = %q, want none", got)
	}
}
