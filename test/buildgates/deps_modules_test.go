package buildgates

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// depsCheckModulesVar is the Makefile variable naming every module
// `make deps-check` and `make deps-bump` walk (ruling 640).
const depsCheckModulesVar = "DEPS_CHECK_MODULES ?="

// makefileDepsCheckModules returns the DEPS_CHECK_MODULES list from the
// Makefile text, following backslash continuation lines.
func makefileDepsCheckModules(makefile string) ([]string, bool) {
	lines := strings.Split(makefile, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, depsCheckModulesVar) {
			continue
		}
		val := strings.TrimPrefix(line, depsCheckModulesVar)
		for strings.HasSuffix(strings.TrimSpace(val), `\`) && i+1 < len(lines) {
			val = strings.TrimSuffix(strings.TrimSpace(val), `\`) + " "
			i++
			val += lines[i]
		}
		return strings.Fields(val), true
	}
	return nil, false
}

// goModuleDirs walks root for every go.mod, skipping .git, vendor,
// node_modules and testdata trees, and returns each module's directory
// relative to root ("." for the root itself).
func goModuleDirs(root string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "go.mod" {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		dirs = append(dirs, filepath.ToSlash(rel))
		return nil
	})
	return dirs, err
}

// moduleSetDiff returns the entries of want missing from got and the
// entries of got not in want, each sorted.
func moduleSetDiff(want, got []string) (missing, extra []string) {
	in := func(s []string) map[string]bool {
		m := make(map[string]bool, len(s))
		for _, v := range s {
			m[v] = true
		}
		return m
	}
	w, g := in(want), in(got)
	for v := range w {
		if !g[v] {
			missing = append(missing, v)
		}
	}
	for v := range g {
		if !w[v] {
			extra = append(extra, v)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

// TestDepsCheckModulesListsEveryModule fails when a go.mod is added or
// removed without the Makefile's DEPS_CHECK_MODULES following it: a module
// missing from the list is never checked or bumped (Codex round 1 P2, #1589).
func TestDepsCheckModulesListsEveryModule(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	listed, ok := makefileDepsCheckModules(string(b))
	if !ok {
		t.Fatalf("Makefile has no %q line", depsCheckModulesVar)
	}
	found, err := goModuleDirs(root)
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	missing, extra := moduleSetDiff(found, listed)
	if len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("DEPS_CHECK_MODULES differs from the repository's go.mod files:\n  missing (a go.mod not listed): %v\n  extra (listed, no go.mod): %v", missing, extra)
	}
}

func TestDepsCheckModulesHelpers_CanFail(t *testing.T) {
	got, ok := makefileDepsCheckModules("X := 1\nDEPS_CHECK_MODULES ?= . a \\\n  b/c\nY := 2\n")
	if !ok || strings.Join(got, " ") != ". a b/c" {
		t.Fatalf("parse: got %v, %v", got, ok)
	}
	if _, ok := makefileDepsCheckModules("X := 1\n"); ok {
		t.Fatal("parse: found a list in a Makefile without one")
	}

	root := t.TempDir()
	for _, d := range []string{".", "sub/m", "x/testdata/t", "vendor/v", "node_modules/n", "sub/noMod"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
		if d == "sub/noMod" {
			continue
		}
		if err := os.WriteFile(filepath.Join(root, d, "go.mod"), []byte("module m\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dirs, err := goModuleDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(dirs)
	if strings.Join(dirs, " ") != ". sub/m" {
		t.Fatalf("walk: got %v, want [. sub/m]", dirs)
	}

	missing, extra := moduleSetDiff(dirs, []string{".", "gone"})
	if strings.Join(missing, " ") != "sub/m" || strings.Join(extra, " ") != "gone" {
		t.Fatalf("diff: missing %v extra %v", missing, extra)
	}
}
