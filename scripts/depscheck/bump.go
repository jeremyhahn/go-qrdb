package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// bumpLookupErrors returns every finding whose latest version could not be
// looked up, joined, or nil. -bump returns it before it edits any
// dependency: a bump that skipped those modules and exited 0 would report
// the tree current while their lag goes unseen.
func bumpLookupErrors(findings []finding) error {
	var errs []error
	for _, f := range findings {
		if f.Lookup != nil {
			errs = append(errs, fmt.Errorf("%s:%d: %w", f.Dep.File, f.Dep.Line, f.Lookup))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	errs = append(errs, fmt.Errorf("deps-bump: FAILED: the latest version of %d module(s) could not be looked up; nothing was bumped", len(errs)))
	return errors.Join(errs...)
}

// bumpable reports whether -bump moves this finding: a require behind its
// latest, with no reason comment, past the fresh window, looked up, used,
// and not a fork onto another module path.
func bumpable(f finding) bool {
	d := f.Dep
	return f.Lookup == nil && !f.Unused && f.Behind && !f.Fresh &&
		d.Reason == "" && d.Path == d.Module && f.Latest != forkLatest
}

// bumpModules runs `go get path@latest` for each bumpable require of every
// module, then `go mod tidy` in each module that moved and in each module
// that replaces a module with a local directory, and prints each move. A replace that would move is
// printed as a hand edit, since go get does not rewrite a replace. A finding
// whose lookup failed returns the lookup errors before any edit. A bump
// whose go get raises a held requirement (one with a reason comment) is
// reverted and printed as skipped, so no hold moves under its old reason.
func bumpModules(edit moduleEditor, mods []moduleFindings, stdout io.Writer) error {
	// Every module is looked up before any is edited: a lookup failure in
	// one stops the bump with the tree untouched.
	var all []finding
	for _, m := range mods {
		all = append(all, m.found...)
	}
	if err := bumpLookupErrors(all); err != nil {
		return err
	}
	type upgraded struct {
		dir   string
		held  map[string]string
		moved int
		local bool
	}
	done := make([]upgraded, 0, len(mods))
	moved := 0
	for _, m := range mods {
		held, n, err := upgradeModule(edit, m.dir, m.found, stdout)
		if err != nil {
			return err
		}
		local, err := replacesLocally(m.dir)
		if err != nil {
			return err
		}
		done = append(done, upgraded{dir: m.dir, held: held, moved: n, local: local})
		moved += n
	}
	if moved == 0 {
		return nil
	}
	// Reconcile once every upgrade is in: modules that moved are tidied
	// first, then every module that replaces another with a local
	// directory, bumped or not, so that a dependent such as one replacing
	// the root with ../.. tracks the root's final graph and stays
	// consistent under -mod=readonly.
	var tidied []upgraded
	for _, local := range []bool{false, true} {
		for _, u := range done {
			if u.local != local || (u.moved == 0 && !u.local) {
				continue
			}
			if err := edit.Tidy(u.dir); err != nil {
				return err
			}
			tidied = append(tidied, u)
		}
	}
	for _, u := range tidied {
		raised, err := raisedHolds(u.dir, u.held)
		if err != nil {
			return err
		}
		if raised != "" {
			return &HeldRaisedError{Dir: u.dir, Raised: raised}
		}
	}
	return nil
}

// replacesLocally reports whether the module in dir replaces a module with
// a local directory, so that an upgrade elsewhere in the tree can move its
// graph without a bump of its own.
func replacesLocally(dir string) (bool, error) {
	directives, err := readGoMod(dir)
	if err != nil {
		return false, err
	}
	for _, d := range directives {
		if d.Kind == kindReplace && d.NewVersion == "" {
			return true, nil
		}
	}
	return false, nil
}

// upgradeModule goes every bumpable finding of the module in dir to its
// latest version, reverting a bump that raises a held requirement. It
// returns the module's held requirements and the number of bumps kept.
func upgradeModule(edit moduleEditor, dir string, findings []finding, stdout io.Writer) (map[string]string, int, error) {
	held, err := heldRequirements(dir)
	if err != nil {
		return nil, 0, err
	}
	moved := 0
	for i := range findings {
		f := &findings[i]
		if !bumpable(*f) {
			continue
		}
		d := f.Dep
		if d.Kind == kindReplace {
			if _, err := fmt.Fprintf(stdout, "%s:%d: replace %s %s -> %s needs a hand edit\n", d.File, d.Line, d.Module, d.Version, f.Latest); err != nil {
				return nil, 0, err
			}
			continue
		}
		// Every target was planned before any edit, but each get runs on
		// the graph earlier bumps left: one of them may already have
		// raised this module to or past its target, and getting the older
		// target would downgrade it and the module whose bump raised it.
		current, err := requiredNow(dir, d.Path)
		if err != nil {
			return nil, 0, err
		}
		if current != d.Version && semver.Compare(current, f.Latest) >= 0 {
			if _, err := fmt.Fprintf(stdout, "bumped %s %s -> %s\n", d.Path, d.Version, current); err != nil {
				return nil, 0, err
			}
			moved++
			continue
		}
		before, err := snapshotModFiles(dir)
		if err != nil {
			return nil, 0, err
		}
		if err := edit.Get(dir, d.Path+"@"+f.Latest); err != nil {
			return nil, 0, err
		}
		raised, err := raisedHolds(dir, held)
		if err != nil {
			return nil, 0, err
		}
		if raised != "" {
			// go get raised a held requirement through the bumped module's
			// own requirements: the hold's reason no longer describes the
			// version, so the bump is reverted and left to a hand edit.
			if err := before.restore(); err != nil {
				return nil, 0, err
			}
			if _, err := fmt.Fprintf(stdout, "skipped %s %s -> %s: it raises %s\n", d.Path, d.Version, f.Latest, raised); err != nil {
				return nil, 0, err
			}
			continue
		}
		if _, err := fmt.Fprintf(stdout, "bumped %s %s -> %s\n", d.Path, d.Version, f.Latest); err != nil {
			return nil, 0, err
		}
		moved++
	}
	return held, moved, nil
}

// HeldRaisedError is go mod tidy raising a held requirement after the bumps.
type HeldRaisedError struct {
	Dir    string
	Raised string
}

func (e *HeldRaisedError) Error() string {
	return fmt.Sprintf("%s: go mod tidy raises %s; move it by hand", e.Dir, e.Raised)
}

// heldRequirements reads dir/go.mod's held requirements: every require,
// direct or indirect, whose comment gives a reason to stay at its version,
// keyed by module path.
func heldRequirements(dir string) (map[string]string, error) {
	directives, err := readGoMod(dir)
	if err != nil {
		return nil, err
	}
	held := make(map[string]string)
	for _, d := range directives {
		if d.Kind == kindRequire && d.Comment != "" {
			held[d.Path] = d.Version
		}
	}
	return held, nil
}

// raisedHolds names, as "held path old -> new" joined by ", ", each held
// requirement whose version in dir/go.mod is no longer the held one. A held
// requirement go.mod no longer lists is not raised.
// requiredNow returns the version dir's go.mod now requires of path,
// which is the version the build selects once go get has written it.
func requiredNow(dir, path string) (string, error) {
	directives, err := readGoMod(dir)
	if err != nil {
		return "", err
	}
	for _, d := range directives {
		if d.Kind == kindRequire && d.Path == path {
			return d.Version, nil
		}
	}
	return "", nil
}

func raisedHolds(dir string, held map[string]string) (string, error) {
	directives, err := readGoMod(dir)
	if err != nil {
		return "", err
	}
	var raised []string
	for _, d := range directives {
		if v, ok := held[d.Path]; ok && d.Kind == kindRequire && d.Version != v {
			raised = append(raised, "held "+d.Path+" "+v+" -> "+d.Version)
		}
	}
	sort.Strings(raised)
	return strings.Join(raised, ", "), nil
}

func readGoMod(dir string) ([]directive, error) {
	gomod := filepath.Join(dir, "go.mod")
	data, err := os.ReadFile(gomod) //#nosec G304 -- the go.mod under check
	if err != nil {
		return nil, err
	}
	return parseGoMod(gomod, data)
}

// modFiles is go.mod and go.sum as they were before a go get; a nil
// content is a file that did not exist.
type modFiles map[string][]byte

func snapshotModFiles(dir string) (modFiles, error) {
	files := make(modFiles)
	for _, name := range []string{"go.mod", "go.sum"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path) //#nosec G304 -- the module under check
		switch {
		case errors.Is(err, os.ErrNotExist):
			files[path] = nil
		case err != nil:
			return nil, err
		default:
			files[path] = data
		}
	}
	return files, nil
}

// restore puts every file back as it was, removing one that did not exist.
func (m modFiles) restore() error {
	for path, data := range m {
		if data == nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// moduleFindings is one module directory's findings, held by -bump until
// every module has been looked up.
type moduleFindings struct {
	dir   string
	found []finding
}
