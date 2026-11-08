// fips_legs_test.go is the gate over CI's FIPS 140-3 legs (#1230).
//
// ci.yml's `fips` job runs the two unit suites, `make test` and `make
// test-sdk`, under GODEBUG=fips140=only as parallel matrix legs, one per name
// in the Makefile's FIPS_LEGS, each `make test-fips-leg FIPS_LEG=<leg>`. The
// job is the evidence the product is FIPS-clean only while the legs together
// are exactly both suites, so this gate fails when:
//
//   - a package of either suite is in no leg, or in two;
//   - a leg runs no package, or a package outside both suites;
//   - ci.yml's matrix is not FIPS_LEGS, lets one leg's failure cancel the
//     others, or its test step does not run the matrix's leg;
//   - a leg has no time limit of its own, one over the hosted runner's
//     ceiling (jobMinutesCeiling), or a go test per-package budget not 5
//     minutes inside it.
//
// Discovery floors on the leg and package counts keep an empty parse from
// passing: every other assertion is inside a loop, and an empty loop reports
// success.
package buildgates

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	fipsLegTarget  = "make test-fips-leg FIPS_LEG="
	fipsLegKey     = "leg"
	fipsLegEnv     = "${{ matrix.leg }}"
	fipsLegFix     = "put it in one UNIT_LEG_<leg> list in the Makefile, or let the remainder leg run it"
	fipsMinutesKey = "minutes"
	fipsMinutesEnv = "${{ matrix.minutes }}"
	// fipsBudget is the step's go test per-package budget: the leg's limit
	// less 5 minutes, so a runaway package is named before the leg is killed.
	fipsBudget = "GOTEST_TIMEOUT=$((FIPS_LEG_MINUTES - 5))m"
	// fipsLegCeiling is the most minutes any leg may run: a hosted job's.
	fipsLegCeiling = jobMinutesCeiling

	// Floors, well under today's counts (12 legs, 191 root and 53 SDK
	// packages), so a broken listing fails rather than checks nothing.
	fipsLegFloor     = 5
	fipsRootPkgFloor = 150
	fipsSDKPkgFloor  = 40
)

// fipsWorkflow is the slice of ci.yml the gate reads.
type fipsWorkflow struct {
	Jobs map[string]struct {
		Strategy struct {
			FailFast *bool     `yaml:"fail-fast"`
			Matrix   yaml.Node `yaml:"matrix"`
		} `yaml:"strategy"`
		TimeoutMinutes string `yaml:"timeout-minutes"`
		Steps          []struct {
			Name string            `yaml:"name"`
			Env  map[string]string `yaml:"env"`
			Run  string            `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// fipsJobProblems checks ci.yml's fips job against the Makefile's legs.
func fipsJobProblems(body []byte, legs []string) []string {
	var wf fipsWorkflow
	if err := yaml.Unmarshal(body, &wf); err != nil {
		return []string{"parse ci.yml: " + err.Error()}
	}
	job, ok := wf.Jobs[fipsJob]
	if !ok {
		return []string{"ci.yml has no " + fipsJob + " job"}
	}
	var problems []string
	if job.Strategy.FailFast == nil || *job.Strategy.FailFast {
		problems = append(problems, fipsJob+" lacks `fail-fast: false`: one failing leg would cancel the "+
			"others, and their packages would go unmeasured")
	}
	if job.TimeoutMinutes != fipsMinutesEnv {
		problems = append(problems, fipsJob+"'s timeout-minutes is "+strconv.Quote(job.TimeoutMinutes)+
			"; want "+fipsMinutesEnv+", each leg's own limit")
	}
	problems = append(problems, fipsMatrixProblems(job.Strategy.Matrix, legs)...)
	runs := 0
	for _, s := range job.Steps {
		if !strings.Contains(s.Run, fipsLegTarget) {
			continue
		}
		runs++
		if s.Env["FIPS_LEG"] != fipsLegEnv || !strings.Contains(s.Run, fipsLegTarget+"'$FIPS_LEG'") {
			problems = append(problems, fipsJob+"'s step "+strconv.Quote(s.Name)+" does not run the matrix's "+
				"leg; want env FIPS_LEG: "+fipsLegEnv+" and "+fipsLegTarget+"'$FIPS_LEG'")
		}
		if s.Env["FIPS_LEG_MINUTES"] != fipsMinutesEnv || !strings.Contains(s.Run, fipsBudget) {
			problems = append(problems, fipsJob+"'s step "+strconv.Quote(s.Name)+" does not set go test's "+
				"per-package budget inside the leg's limit; want env FIPS_LEG_MINUTES: "+fipsMinutesEnv+
				" and "+fipsBudget)
		}
	}
	if runs != 1 {
		problems = append(problems, fipsJob+" has "+strconv.Itoa(runs)+" steps running "+
			strconv.Quote(fipsLegTarget)+"; want exactly one")
	}
	return problems
}

// fipsMatrixProblems checks the matrix is one literal `include:` list naming
// every Makefile leg once, each with its own limit.
func fipsMatrixProblems(m yaml.Node, legs []string) []string {
	if m.Kind != yaml.MappingNode || len(m.Content) != 2 || m.Content[0].Value != "include" {
		return []string{fipsJob + "'s matrix is not one `include:` list of {" + fipsLegKey + ", " +
			fipsMinutesKey + "} entries: any other axis multiplies or edits the legs"}
	}
	if m.Content[1].Kind != yaml.SequenceNode {
		return []string{fipsJob + "'s matrix include is not a literal list"}
	}
	var problems []string
	var got []string
	for _, e := range m.Content[1].Content {
		leg, entry := fipsEntryProblems(e)
		problems = append(problems, entry...)
		if leg == "" {
			continue
		}
		if slices.Contains(got, leg) {
			problems = append(problems, fipsJob+"'s matrix names leg "+strconv.Quote(leg)+" twice")
			continue
		}
		got = append(got, leg)
		if !slices.Contains(legs, leg) {
			problems = append(problems, fipsJob+"'s matrix names leg "+strconv.Quote(leg)+
				", which `make -s print-fips-legs` does not print; fix: delete it from the matrix")
		}
	}
	for _, l := range legs {
		if !slices.Contains(got, l) {
			problems = append(problems, fipsJob+"'s matrix runs no leg "+strconv.Quote(l)+
				", which `make -s print-fips-legs` prints; fix: add it to the matrix")
		}
	}
	return problems
}

// fipsEntryProblems checks one include entry is exactly a leg name and its
// limit in minutes: more than the 5-minute margin go test's budget is cut by,
// and at most fipsLegCeiling. It returns the leg, empty when there is none.
func fipsEntryProblems(e *yaml.Node) (string, []string) {
	if e.Kind != yaml.MappingNode {
		return "", []string{fipsJob + "'s matrix include has an entry that is not a mapping"}
	}
	var leg, minutes string
	var problems []string
	for i := 0; i+1 < len(e.Content); i += 2 {
		switch key, val := e.Content[i].Value, e.Content[i+1].Value; key {
		case fipsLegKey:
			leg = val
		case fipsMinutesKey:
			minutes = val
		default:
			problems = append(problems, fipsJob+"'s matrix entry has key "+strconv.Quote(key)+
				"; want only "+fipsLegKey+" and "+fipsMinutesKey)
		}
	}
	if leg == "" {
		return "", append(problems, fipsJob+"'s matrix has an entry with no "+fipsLegKey)
	}
	if n, err := strconv.Atoi(minutes); err != nil || n <= 5 || n > fipsLegCeiling {
		problems = append(problems, fipsJob+"'s leg "+strconv.Quote(leg)+" has "+fipsMinutesKey+" "+
			strconv.Quote(minutes)+"; want a whole number over 5 and at most "+strconv.Itoa(fipsLegCeiling))
	}
	return leg, problems
}

// fipsPartitionProblems checks the legs' packages are the suite's, each once.
func fipsPartitionProblems(legs []string, legPkgs map[string][]string, split map[string]bool, suite []string) []string {
	var problems []string
	legOf := map[string]string{}
	for _, l := range legs {
		if len(legPkgs[l]) == 0 {
			problems = append(problems, "FIPS leg "+strconv.Quote(l)+" runs no package; fix: delete it "+
				"from UNIT_LEGS or correct its UNIT_LEG_"+l+" patterns")
		}
		for _, p := range legPkgs[l] {
			if prev, dup := legOf[p]; dup {
				// A package split by test name is in each of its legs;
				// fipsSplitProblems checks each of its tests is in one.
				if !split[prev] || !split[l] {
					problems = append(problems, p+" runs in FIPS leg "+prev+" and again in leg "+l+
						"; fix: delete it from one leg's UNIT_LEG_<leg> patterns, or split it by test name in both")
				}
				continue
			}
			legOf[p] = l
			if !slices.Contains(suite, p) {
				problems = append(problems, "FIPS leg "+l+" runs "+p+", which neither `make test` nor "+
					"`make test-sdk` runs; fix: delete it from UNIT_LEG_"+l)
			}
		}
	}
	for _, p := range suite {
		if _, ok := legOf[p]; !ok {
			problems = append(problems, p+" is in no FIPS leg; fix: "+fipsLegFix)
		}
	}
	return problems
}

// goList lists the import paths patterns match in dir under tags.
func goList(t *testing.T, dir, tags string, patterns ...string) []string {
	t.Helper()
	pkgs, err := listPackages(dir, tags, patterns...)
	if err != nil {
		t.Fatal(err)
	}
	return pkgs
}

// goListError is a failed go list, carrying the stderr that names its cause:
// an exit status alone says nothing about which package or file failed.
type goListError struct {
	dir      string
	patterns []string
	err      error
	stderr   string
}

func (e *goListError) Error() string {
	return "go list " + strings.Join(e.patterns, " ") + " in " + e.dir + ": " + e.err.Error() + "\n" + e.stderr
}

func (e *goListError) Unwrap() error { return e.err }

// listPackages runs go list under tags in dir and returns the import paths it
// prints, or a *goListError holding go list's stderr.
func listPackages(dir, tags string, patterns ...string) ([]string, error) {
	cmd := exec.Command("go", append([]string{"list", "-tags", tags}, patterns...)...) // #nosec G204 -- patterns come from the Makefile's own TEST_PKGS
	cmd.Dir = dir
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, &goListError{dir: dir, patterns: patterns, err: err, stderr: errOut.String()}
	}
	return strings.Fields(out.String()), nil
}

// TestFIPSLegs_ListPackagesReportsStderr proves a failing go list's own
// message reaches the gate's failure text, not only its exit status: run
// 70728 failed with "exit status 1" and nothing to say which package broke.
func TestFIPSLegs_ListPackagesReportsStderr(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/broken\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte("packag broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := listPackages(dir, "", "./...")
	var gle *goListError
	if !errors.As(err, &gle) {
		t.Fatalf("listPackages on a broken package: err = %v, want a *goListError", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("goListError does not unwrap to the *exec.ExitError: %v", err)
	}
	for _, want := range []string{"broken.go", "expected 'package'", "in " + dir} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure text lacks %q:\n%s", want, err)
		}
	}
}

// TestFIPSLegs_ListPackagesListsAPackage is the passing case: the import paths
// go list prints come back, and stderr is not mixed into them.
func TestFIPSLegs_ListPackagesListsAPackage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/fine\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fine.go"), []byte("package fine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pkgs, err := listPackages(dir, "", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pkgs, []string{"example.com/fine"}) {
		t.Errorf("listPackages = %q, want [example.com/fine]", pkgs)
	}
}

func TestFIPSLegs_PartitionTheSuites(t *testing.T) {
	root := repoRoot(t)
	legs := strings.Fields(makeOutput(t, root, "print-fips-legs"))
	if len(legs) < fipsLegFloor {
		t.Fatalf("`make -s print-fips-legs` printed %d legs, below the floor of %d", len(legs), fipsLegFloor)
	}

	tags := makeOutput(t, root, "print-build-tags")
	rootPkgs := goList(t, root, tags, strings.Fields(makeOutput(t, root, "print-test-pkgs"))...)
	sdkPkgs := goList(t, filepath.Join(root, "sdk", "go"), tags, "./...")
	if len(rootPkgs) < fipsRootPkgFloor || len(sdkPkgs) < fipsSDKPkgFloor {
		t.Fatalf("listed %d root and %d SDK packages, below the floors of %d and %d",
			len(rootPkgs), len(sdkPkgs), fipsRootPkgFloor, fipsSDKPkgFloor)
	}

	legPkgs := make(map[string][]string, len(legs))
	split := make(map[string]bool, len(legs))
	for _, l := range legs {
		legPkgs[l] = strings.Fields(makeOutput(t, root, "print-unit-leg-packages", "UNIT_LEG="+l))
		split[l] = fipsLegFilter(t, root, l).split()
	}
	for _, p := range fipsPartitionProblems(legs, legPkgs, split, append(rootPkgs, sdkPkgs...)) {
		t.Error(p)
	}

	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	for _, p := range fipsJobProblems(body, legs) {
		t.Error(p)
	}
}

func TestFIPSLegs_PartitionProblems(t *testing.T) {
	legs := []string{"a", "rest"}
	suite := []string{"p1", "p2", "p3"}
	cases := []struct {
		name    string
		legPkgs map[string][]string
		split   map[string]bool
		want    string
	}{
		{"valid", map[string][]string{"a": {"p1"}, "rest": {"p2", "p3"}}, nil, ""},
		{"missing", map[string][]string{"a": {"p1"}, "rest": {"p2"}}, nil, "p3 is in no FIPS leg"},
		{"duplicate", map[string][]string{"a": {"p1", "p2"}, "rest": {"p2", "p3"}}, nil, "p2 runs in FIPS leg a and again in leg rest"},
		{"split in both", map[string][]string{"a": {"p1"}, "rest": {"p1", "p2", "p3"}}, map[string]bool{"a": true, "rest": true}, ""},
		{"split in one", map[string][]string{"a": {"p1"}, "rest": {"p1", "p2", "p3"}}, map[string]bool{"a": true}, "p1 runs in FIPS leg a and again in leg rest"},
		{"empty leg", map[string][]string{"a": nil, "rest": {"p1", "p2", "p3"}}, nil, `FIPS leg "a" runs no package`},
		{"outside the suites", map[string][]string{"a": {"p1", "x"}, "rest": {"p2", "p3"}}, nil, "FIPS leg a runs x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := fipsPartitionProblems(legs, c.legPkgs, c.split, suite)
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

const fipsJobFixture = `jobs:
  fips:
    strategy:
      fail-fast: false
      matrix:
        include:
          - leg: a
            minutes: 45
          - leg: rest
            minutes: 30
    timeout-minutes: ${{ matrix.minutes }}
    steps:
      - name: Unit tests
        env:
          FIPS_LEG: ${{ matrix.leg }}
          FIPS_LEG_MINUTES: ${{ matrix.minutes }}
        run: make test-fips-leg FIPS_LEG='$FIPS_LEG' GOTEST_TIMEOUT=$((FIPS_LEG_MINUTES - 5))m
`

func TestFIPSLegs_JobProblems(t *testing.T) {
	legs := []string{"a", "rest"}
	if p := fipsJobProblems([]byte(fipsJobFixture), legs); len(p) != 0 {
		t.Fatalf("valid fixture: want no problems, got %q", p)
	}
	cases := []struct {
		name, old, new, want string
	}{
		{"leg missing", "          - leg: rest\n            minutes: 30\n", "", `runs no leg "rest"`},
		{"unknown leg", "leg: rest", "leg: b", `names leg "b"`},
		{"leg twice", "leg: rest", "leg: a", `names leg "a" twice`},
		{"extra axis", "        include:", "        os: [x]\n        include:", "not one `include:` list"},
		{"extra key", "minutes: 30", "minutes: 30\n            os: x", `has key "os"`},
		{"no leg", "- leg: rest", "- os: rest", "entry with no leg"},
		{"computed matrix", "include:\n          - leg: a\n            minutes: 45\n          - leg: rest\n            minutes: 30", "include: ${{ fromJSON(needs.x.outputs.legs) }}", "is not a literal list"},
		{"over ceiling", "minutes: 45", "minutes: 351", `"a" has minutes "351"`},
		{"under budget margin", "minutes: 30", "minutes: 5", `"rest" has minutes "5"`},
		{"no minutes", "\n            minutes: 30", "", `"rest" has minutes ""`},
		{"fixed limit", "timeout-minutes: ${{ matrix.minutes }}", "timeout-minutes: 45", "timeout-minutes is \"45\""},
		{"fail-fast", "fail-fast: false", "fail-fast: true", "fail-fast: false"},
		{"fixed leg", "FIPS_LEG='$FIPS_LEG'", "FIPS_LEG=a", "does not run the matrix's leg"},
		{"fixed budget", "GOTEST_TIMEOUT=$((FIPS_LEG_MINUTES - 5))m", "GOTEST_TIMEOUT=40m", "per-package budget"},
		{"no minutes env", "          FIPS_LEG_MINUTES: ${{ matrix.minutes }}\n", "", "per-package budget"},
		{"no leg step", "make test-fips-leg", "make test", "0 steps running"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := strings.Replace(fipsJobFixture, c.old, c.new, 1)
			if body == fipsJobFixture {
				t.Fatalf("mutation %q did not apply", c.old)
			}
			p := fipsJobProblems([]byte(body), legs)
			if !slices.ContainsFunc(p, func(s string) bool { return strings.Contains(s, c.want) }) {
				t.Fatalf("want a problem containing %q, got %q", c.want, p)
			}
		})
	}
}
