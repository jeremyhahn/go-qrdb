package main

import (
	"errors"
	"fmt"
	"io"
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

// bumpModule runs `go get path@latest` for each bumpable require in dir,
// then `go mod tidy`, and prints each move. A replace that would move is
// printed as a hand edit, since go get does not rewrite a replace. A finding
// whose lookup failed returns the lookup errors before any edit.
func bumpModule(edit moduleEditor, dir string, findings []finding, stdout io.Writer) error {
	if err := bumpLookupErrors(findings); err != nil {
		return err
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
				return err
			}
			continue
		}
		if err := edit.Get(dir, d.Path+"@"+f.Latest); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "bumped %s %s -> %s\n", d.Path, d.Version, f.Latest); err != nil {
			return err
		}
		moved++
	}
	if moved == 0 {
		return nil
	}
	return edit.Tidy(dir)
}

// moduleFindings is one module directory's findings, held by -bump until
// every module has been looked up.
type moduleFindings struct {
	dir   string
	found []finding
}
