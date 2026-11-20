package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGovulncheckScansPastUnpublishedV0 runs the real govulncheck, offline,
// on the graph test/performance/cmd/kvcompare has: app requires only perf, whose
// published go.mod requires core at v0.0.0, a version never published
// because perf builds against a local replacement its consumers do not
// inherit. The v0.0.0 entry must not abort the scan, the selected versions
// of the real requirements must still be scanned, and an advisory that
// covers only the unpublished v0.0.0 must not be reported.
//
// The fixture is a pre-filled module cache (GOPROXY=off) and a file://
// advisory database, both under testdata/unpublished.
func TestGovulncheckScansPastUnpublishedV0(t *testing.T) {
	path, err := exec.LookPath("govulncheck")
	if err != nil {
		t.Skip("govulncheck is not on PATH")
	}
	root := unpublishedFixture(t)

	scanner := govulncheck{path: path, db: "file://" + filepath.ToSlash(filepath.Join(root, "vulndb"))}
	hits, err := scanner.Scan(filepath.Join(root, "app"))
	if verr := (*VulnError)(nil); errors.As(err, &verr) {
		t.Fatalf("govulncheck aborted on the unpublished v0.0.0 requirement: %v\n%s", err, verr.Stderr)
	}
	require.NoError(t, err)

	assert.Equal(t, map[string][]advisory{
		"example.com/perf@v1.0.0": {{ID: "GO-2026-9001", Fixed: "v1.1.0"}},
	}, hits)
	assert.NotContains(t, hits, "example.com/core@v0.0.0")
}

// TestUnpublishedFixtureSelectsV0 keeps the scan test honest: the graph
// the fixture's app builds must select core at the unpublished v0.0.0, as
// kvcompare's does. A consumer requirement on a published core would win
// minimum version selection and leave the scan nothing unpublished to
// survive.
func TestUnpublishedFixtureSelectsV0(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := unpublishedFixture(t)

	cmd := exec.CommandContext(t.Context(), "go", "list", "-m", "-e", "-f", "{{.Path}}@{{.Version}}", "all")
	cmd.Dir = filepath.Join(root, "app")
	out, err := cmd.Output()
	require.NoError(t, err)

	assert.Equal(t, []string{"example.com/app@", "example.com/core@v0.0.0", "example.com/perf@v1.0.0"},
		strings.Fields(string(out)))
}

// unpublishedFixture copies testdata/unpublished to a temporary directory
// and points the go command at its module cache, offline.
func unpublishedFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.CopyFS(root, os.DirFS(filepath.Join("testdata", "unpublished"))))
	t.Setenv("GOMODCACHE", filepath.Join(root, "modcache"))
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOFLAGS", "-mod=readonly")
	t.Setenv("GOTOOLCHAIN", "local")
	return root
}
