package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// listing is one fixture module for list.json: behind when latest differs.
func listing(path, version, latest string) string {
	s := `{"Path": "` + path + `", "Version": "` + version + `", "Versions": ["v1.0.0", "v1.1.0"]`
	if latest != version {
		s += `, "Update": {"Path": "` + path + `", "Version": "` + latest + `", "Time": "2026-09-01T12:00:00Z"}`
	}
	return s + "}\n"
}

// workflowDepsCheckArgs is the depscheck argument list the deps-bump.yml
// bump step's `make deps-check` invocation turns into through the Makefile
// (DEPS_CHECK_BASE=X becomes -base X; the target always passes -vuln).
func workflowDepsCheckArgs(t *testing.T, dir string) []string {
	t.Helper()
	wf, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "deps-bump.yml"))
	require.NoError(t, err)
	var line string
	for _, l := range strings.Split(string(wf), "\n") {
		if strings.Contains(l, "make deps-bump && make deps-check") {
			line = l
			break
		}
	}
	require.NotEmpty(t, line, "deps-bump.yml runs make deps-check after make deps-bump")
	call := line[strings.Index(line, "make deps-check")+len("make deps-check"):]
	call, _, _ = strings.Cut(call, "&&")
	var args []string
	for _, f := range strings.Fields(call) {
		if v, ok := strings.CutPrefix(f, "DEPS_CHECK_BASE="); ok {
			args = append(args, "-base", v)
		}
	}
	return append(args, "-vuln", dir)
}

// TestBumpRunExportsIndependentUpgradesPastAHeldConflict follows
// TestBumpNeverMovesAHeldRequirement's scenario through deps-bump.yml: the
// bump skips a (it would raise the held b) and upgrades c, then the
// workflow's own deps-check invocation must pass, or the step exits before
// the patch carrying c is exported. a stays behind with no reason on its
// require line, so only a check against the pre-bump commit passes it.
func TestBumpRunExportsIndependentUpgradesPastAHeldConflict(t *testing.T) {
	dir := t.TempDir()
	before, err := os.ReadFile(filepath.Join("testdata", "heldgraph", "go.mod"))
	require.NoError(t, err)
	writeRootFile(t, dir, "go.mod", before)
	writeRootFile(t, dir, "go.sum", []byte("example.com/a v1.0.0 h1:fake=\n"))
	writeList := func(c string) {
		t.Helper()
		list := listing("example.com/a", "v1.0.0", "v1.1.0") +
			listing("example.com/b", "v1.0.0", "v1.1.0") +
			listing("example.com/c", c, "v1.1.0")
		writeRootFile(t, dir, "list.json", []byte(list))
	}
	src := fixtureSource{goproxy: "direct"}

	writeList("v1.0.0")
	edit := &graphEditor{graph: map[string]map[string]string{
		"example.com/a@v1.1.0": {"example.com/b": "v1.1.0"},
	}}
	code, stdout, stderr := runTools(t, toolset{src: src, edit: edit}, "-bump", dir)
	require.Equal(t, exitOK, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)
	require.Contains(t, stdout, "skipped example.com/a v1.0.0 -> v1.1.0")
	require.Equal(t, "v1.1.0", requiredVersion(t, dir, "example.com/c"))
	require.Equal(t, "v1.0.0", requiredVersion(t, dir, "example.com/a"))

	// The go command now lists the bumped tree: c current, a still behind.
	writeList("v1.1.0")
	args := workflowDepsCheckArgs(t, dir)
	require.Contains(t, args, "-base", "deps-bump.yml verifies the bump against the pre-bump commit")
	base := &fakeBase{data: string(before), found: true}
	code, stdout, stderr = runTools(t, toolset{src: src, base: base, vuln: fakeVuln{}}, args...)
	assert.Equal(t, exitOK, code, "the held conflict's skip must not fail the verify\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	assert.NotEmpty(t, base.asked, "the verify reads the pre-bump go.mod")

	// A vulnerability still fails the same verify.
	hit := map[string][]advisory{"example.com/c@v1.1.0": {{ID: "GO-2026-0640"}}}
	code, _, _ = runTools(t, toolset{src: src, base: &fakeBase{data: string(before), found: true}, vuln: fakeVuln{hits: hit}}, args...)
	assert.Equal(t, exitVulnerable, code, "the verify keeps the vulnerability checks")
}
