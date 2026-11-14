package buildgates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// publishedModules are the modules an embedder requires: go-qrdb and its Go
// SDK. A replace directive applies only to the main module being built, so an
// embedder of either never sees theirs.
var publishedModules = []string{".", "sdk/go"}

// thirdPartyDirectoryReplaces returns the directory replaces in dir's go.mod
// whose module is not go-qrdb's own: code the module builds against that an
// embedder would not get.
func thirdPartyDirectoryReplaces(t *testing.T, dir string) []modReplace {
	t.Helper()
	var out []modReplace
	for _, r := range fileReplaces(t, dir) {
		if r.Old.Path != localModule && !strings.HasPrefix(r.Old.Path, localModule+"/") {
			out = append(out, r)
		}
	}
	return out
}

// TestSDK_NoThirdPartyDirectoryReplace holds go-qrdb and sdk/go to building
// the same third-party code their embedders build: a patched dependency is a
// package of the go-qrdb module (internal/webtransport), never a replace.
func TestSDK_NoThirdPartyDirectoryReplace(t *testing.T) {
	root := repoRoot(t)
	for _, m := range publishedModules {
		for _, r := range thirdPartyDirectoryReplaces(t, filepath.Join(root, filepath.FromSlash(m))) {
			t.Errorf("%s/go.mod: replace %s => %s is not inherited by an embedder; move the code into the go-qrdb module", m, r.Old.Path, r.New.Path)
		}
	}
}

func TestSDK_NoThirdPartyDirectoryReplace_Detects(t *testing.T) {
	dir := t.TempDir()
	mod := "module " + sdkModulePrefix + "\n\ngo 1.26.0\n\n" +
		"replace " + localModule + " => ../..\n\n" +
		"replace example.com/upstream => ./third_party/upstream\n\n" +
		"replace example.com/pinned => example.com/fork v1.0.0\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o600); err != nil {
		t.Fatal(err)
	}
	got := thirdPartyDirectoryReplaces(t, dir)
	if len(got) != 1 || got[0].Old.Path != "example.com/upstream" {
		t.Fatalf("third-party directory replaces = %+v, want only example.com/upstream", got)
	}
}
