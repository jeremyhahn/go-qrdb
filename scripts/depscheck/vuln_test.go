package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGovulncheckScansPastUnpublishedV0 runs the real govulncheck, offline,
// on the graph test/performance/cmd/kvcompare has: app requires perf, whose
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
	root := t.TempDir()
	require.NoError(t, os.CopyFS(root, os.DirFS(filepath.Join("testdata", "unpublished"))))
	t.Setenv("GOMODCACHE", filepath.Join(root, "modcache"))
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOFLAGS", "-mod=readonly")
	t.Setenv("GOTOOLCHAIN", "local")

	scanner := govulncheck{path: path, db: "file://" + filepath.ToSlash(filepath.Join(root, "vulndb"))}
	hits, err := scanner.Scan(filepath.Join(root, "app"))
	if verr := (*VulnError)(nil); errors.As(err, &verr) {
		t.Fatalf("govulncheck aborted on the unpublished v0.0.0 requirement: %v\n%s", err, verr.Stderr)
	}
	require.NoError(t, err)

	assert.Equal(t, map[string][]advisory{
		"example.com/dep@v1.0.0": {{ID: "GO-2026-9001", Fixed: "v1.1.0"}},
	}, hits)
	assert.NotContains(t, hits, "example.com/core@v0.0.0")
}
