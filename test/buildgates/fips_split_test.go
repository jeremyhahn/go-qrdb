package buildgates

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// A package that runs past a leg's limit on its own under FIPS 140-3 mode is
// split by test name (#1234): each of its legs names the one package and
// selects top-level tests with `go test -run` and `-skip`. The gate below
// fails when a test function of a split package is selected by no leg or by
// more than one, so a new test cannot fall out of the FIPS run.

const (
	// Floors, well under today's counts (3 split packages, over 300 tests
	// in each), so a broken listing fails rather than checks nothing.
	fipsSplitPkgFloor  = 2
	fipsSplitTestFloor = 200

	fipsSplitFix = "fix: name it in one UNIT_RUN_<leg> of its package's legs in the Makefile, or let the " +
		"package's UNIT_SKIP_ leg run it"
)

// fipsFilter is one leg's test selection: go test's -run and -skip patterns.
type fipsFilter struct {
	run, skip string
}

// split reports whether the leg selects tests by name.
func (f fipsFilter) split() bool {
	return f.run != "" || f.skip != ""
}

// fipsLegFilter reads leg's selection from `make -s print-fips-leg-filter`,
// which prints the patterns test-fips-leg passes to go test.
func fipsLegFilter(t *testing.T, root, leg string) fipsFilter {
	t.Helper()
	var f fipsFilter
	for line := range strings.Lines(makeOutput(t, root, "print-fips-leg-filter", "FIPS_LEG="+leg)) {
		key, val, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "run":
			f.run = val
		case "skip":
			f.skip = val
		default:
			t.Fatalf("make -s print-fips-leg-filter FIPS_LEG=%s printed %q; want run= and skip= lines", leg, line)
		}
	}
	return f
}

// fipsPatternProblem checks one -run or -skip pattern compiles and is one
// that every reader takes alike: no '/' (go test would match it against
// subtests), and no quote, comma or space (the Makefile quotes it for the
// shell and passes it through $(if ...)).
func fipsPatternProblem(leg, flag, pattern string) (*regexp.Regexp, string) {
	if pattern == "" {
		return nil, ""
	}
	if strings.ContainsAny(pattern, "/'\", \t") {
		return nil, "FIPS leg " + leg + "'s " + flag + " pattern " + strconv.Quote(pattern) +
			" holds a '/', quote, comma or space; fix: select top-level tests by name alone"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, "FIPS leg " + leg + "'s " + flag + " pattern does not compile: " + err.Error()
	}
	return re, ""
}

// fipsSplitProblems checks every test of a split package runs in exactly one
// of the legs naming it. filters holds the split legs' selections, legPkgs
// each split leg's packages and tests each split package's top-level tests.
func fipsSplitProblems(filters map[string]fipsFilter, legPkgs map[string][]string, tests map[string][]string) []string {
	var problems []string
	type matcher struct {
		leg       string
		run, skip *regexp.Regexp
	}
	byPkg := map[string][]matcher{}
	for _, l := range slices.Sorted(maps.Keys(filters)) {
		f := filters[l]
		run, p1 := fipsPatternProblem(l, "-run", f.run)
		skip, p2 := fipsPatternProblem(l, "-skip", f.skip)
		if p1 != "" || p2 != "" {
			problems = append(problems, slices.DeleteFunc([]string{p1, p2}, func(s string) bool { return s == "" })...)
			continue
		}
		if len(legPkgs[l]) != 1 {
			problems = append(problems, "FIPS leg "+l+" selects tests by name but runs "+
				strconv.Itoa(len(legPkgs[l]))+" packages; fix: name its one package, a pattern without /...")
			continue
		}
		p := legPkgs[l][0]
		byPkg[p] = append(byPkg[p], matcher{leg: l, run: run, skip: skip})
	}
	for _, p := range slices.Sorted(maps.Keys(byPkg)) {
		for _, name := range tests[p] {
			var in []string
			for _, m := range byPkg[p] {
				if (m.run == nil || m.run.MatchString(name)) && (m.skip == nil || !m.skip.MatchString(name)) {
					in = append(in, m.leg)
				}
			}
			switch {
			case len(in) == 0:
				problems = append(problems, p+"."+name+" runs in no FIPS leg; "+fipsSplitFix)
			case len(in) > 1:
				problems = append(problems, p+"."+name+" runs in FIPS legs "+strings.Join(in, " and ")+
					"; fix: narrow the UNIT_RUN_<leg> patterns so one selects it")
			}
		}
	}
	return problems
}

// fipsTopLevelTest reports whether a top-level function is one go test runs
// and -run selects: cmd/go's rule, a Test, Fuzz or Example prefix followed by
// the end of the name or a character that is not a lower-case letter.
// TestMain is the harness, not a test.
func fipsTopLevelTest(name string) bool {
	if name == "TestMain" {
		return false
	}
	for _, prefix := range []string{"Test", "Fuzz", "Example"} {
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		if rest == "" {
			return true
		}
		r, _ := utf8.DecodeRuneInString(rest)
		return !unicode.IsLower(r)
	}
	return false
}

// fipsTestNames lists pkg's top-level tests under tags: the functions
// fipsTopLevelTest accepts in the test files go list selects.
func fipsTestNames(t *testing.T, root, tags, pkg string) []string {
	t.Helper()
	fields := goList(t, root, tags, "-f", "{{.Dir}}{{range .TestGoFiles}} {{.}}{{end}}{{range .XTestGoFiles}} {{.}}{{end}}", pkg)
	if len(fields) == 0 {
		t.Fatalf("go list %s printed nothing", pkg)
	}
	fset := token.NewFileSet()
	var names []string
	for _, f := range fields[1:] {
		file, err := parser.ParseFile(fset, filepath.Join(fields[0], f), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, d := range file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fipsTopLevelTest(fd.Name.Name) {
				names = append(names, fd.Name.Name)
			}
		}
	}
	return names
}

func TestFIPSLegs_SplitPackagesRunEachTestOnce(t *testing.T) {
	root := repoRoot(t)
	tags := makeOutput(t, root, "print-build-tags")
	filters := map[string]fipsFilter{}
	legPkgs := map[string][]string{}
	for _, l := range strings.Fields(makeOutput(t, root, "print-fips-legs")) {
		if f := fipsLegFilter(t, root, l); f.split() {
			filters[l] = f
			legPkgs[l] = strings.Fields(makeOutput(t, root, "print-unit-leg-packages", "UNIT_LEG="+l))
		}
	}
	tests := map[string][]string{}
	for _, pkgs := range legPkgs {
		for _, p := range pkgs {
			if _, done := tests[p]; done {
				continue
			}
			tests[p] = fipsTestNames(t, root, tags, p)
			if len(tests[p]) < fipsSplitTestFloor {
				t.Fatalf("listed %d tests in split package %s, below the floor of %d", len(tests[p]), p, fipsSplitTestFloor)
			}
		}
	}
	if len(tests) < fipsSplitPkgFloor {
		t.Fatalf("%d packages are split by test name, below the floor of %d", len(tests), fipsSplitPkgFloor)
	}
	for _, p := range fipsSplitProblems(filters, legPkgs, tests) {
		t.Error(p)
	}
}

func TestFIPSLegs_SplitProblems(t *testing.T) {
	legPkgs := map[string][]string{"a": {"p"}, "b": {"p"}, "rest": {"p"}}
	tests := map[string][]string{"p": {"TestAlpha", "TestBeta", "TestGamma", "FuzzDelta"}}
	cases := []struct {
		name    string
		filters map[string]fipsFilter
		legPkgs map[string][]string
		want    string
	}{
		{"valid", map[string]fipsFilter{"a": {run: "^TestAlpha"}, "b": {run: "^TestBeta"}, "rest": {skip: "^TestAlpha|^TestBeta"}}, nil, ""},
		{"in no leg", map[string]fipsFilter{"a": {run: "^TestAlpha"}, "b": {run: "^TestBeta"}}, nil, "p.TestGamma runs in no FIPS leg"},
		{"in two legs", map[string]fipsFilter{"a": {run: "^TestAlpha"}, "b": {run: "^Test(Alpha|Beta)"}, "rest": {skip: "^TestAlpha|^TestBeta"}}, nil, "p.TestAlpha runs in FIPS legs a and b"},
		{"skip leaks", map[string]fipsFilter{"a": {run: "^TestAlpha"}, "rest": {skip: "^TestBeta"}}, nil, "p.TestAlpha runs in FIPS legs a and rest"},
		{"bad regexp", map[string]fipsFilter{"a": {run: "^Test("}}, nil, "does not compile"},
		{"subtest pattern", map[string]fipsFilter{"a": {run: "^TestAlpha/x"}}, nil, "holds a '/'"},
		{"quoted pattern", map[string]fipsFilter{"a": {run: "^TestAlpha'"}}, nil, "holds a '/'"},
		{"two packages", map[string]fipsFilter{"a": {run: "^TestAlpha"}}, map[string][]string{"a": {"p", "q"}}, "runs 2 packages"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pkgs := legPkgs
			if c.legPkgs != nil {
				pkgs = c.legPkgs
			}
			p := fipsSplitProblems(c.filters, pkgs, tests)
			if c.want == "" {
				if len(p) != 0 {
					t.Fatalf("want no problems, got %q", p)
				}
				return
			}
			if !slices.ContainsFunc(p, func(s string) bool { return strings.Contains(s, c.want) }) {
				t.Fatalf("want a problem containing %q, got %q", c.want, p)
			}
		})
	}
}

func TestFIPSLegs_TopLevelTest(t *testing.T) {
	for name, want := range map[string]bool{
		"TestAlpha": true, "Test": true, "Test_x": true, "FuzzBeta": true, "ExampleGamma": true, "Example_x": true,
		"TestMain": false, "Testing": false, "helper": false, "BenchmarkAlpha": false, "Fuzzy": false,
	} {
		if got := fipsTopLevelTest(name); got != want {
			t.Errorf("fipsTopLevelTest(%q) = %v; want %v", name, got, want)
		}
	}
}
