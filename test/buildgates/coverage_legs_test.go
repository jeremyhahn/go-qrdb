// coverage_legs_test.go is the gate over the sharded unit suites (DS-7377).
//
// `make coverage` is one repo-wide gate, and on a 4-vCPU hosted runner its
// race suite outlasts a job's 6 hours. So ci.yml and release.yml measure it
// in matrix legs, one per name in the Makefile's COVER_LEGS, each
// `make coverage-leg COVER_LEG=<leg>` uploading its profile, and ONE merge
// job downloads every profile and runs `make coverage-merge`, the threshold
// and the baseline ratchet. ci.yml's test-sdk job is sharded the same way
// over TEST_SDK_LEGS. The gate is whole only while the legs are exactly the
// Makefile's lists, so this test fails when:
//
//   - a matrix is not the Makefile's list (`make -s print-coverage-legs`,
//     `make -s print-test-sdk-legs`), names a leg twice, or adds an axis;
//   - a matrix lets one leg's failure cancel the others;
//   - a leg's limit is not a literal number of minutes at most
//     jobMinutesCeiling, or its go test per-package budget is not that limit
//     less 5 minutes;
//   - a leg's step does not run the matrix's leg, or a coverage leg does not
//     upload its profile (and, in the toolchain image, copy it out);
//   - the merge job does not need the legs, download every profile or run
//     `make coverage-merge`.
package buildgates

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	coverLegFloor   = 5
	sdkLegFloor     = 2
	coverProfileArt = "coverage-leg-${{ matrix.leg }}"
	coverProfile    = "coverage/leg-${{ matrix.leg }}.out"
	legMatrixEnv    = "${{ matrix.leg }}"
	legBudgetMargin = 5
)

// legJob describes one sharded job a workflow must carry.
type legJob struct {
	workflow, job string
	// printTarget prints the Makefile's leg list.
	printTarget string
	// target is the make invocation the leg's step runs, up to the leg.
	target string
	// legVar is the step env variable carrying the leg.
	legVar string
	// profile requires the coverage profile's upload; copyOut also requires
	// it to be copied out of the toolchain image's container.
	profile, copyOut bool
}

// mergeJob describes the coverage gate over a workflow's legs.
type mergeJob struct {
	workflow, job, legs string
}

var (
	legJobs = []legJob{
		{"ci.yml", "coverage", "print-coverage-legs", "coverage-leg COVER_LEG=", "COVER_LEG", true, true},
		{"release.yml", "coverage", "print-coverage-legs", "coverage-leg COVER_LEG=", "COVER_LEG", true, false},
		{"ci.yml", "test-sdk", "print-test-sdk-legs", "test-sdk-leg SDK_LEG=", "SDK_LEG", false, false},
	}
	mergeJobs = []mergeJob{
		{"ci.yml", "coverage-merge", "coverage"},
		{"release.yml", "coverage-merge", "coverage"},
	}
)

type legStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With map[string]string `yaml:"with"`
}

type legWorkflow struct {
	Jobs map[string]struct {
		Needs    yaml.Node `yaml:"needs"`
		Timeout  string    `yaml:"timeout-minutes"`
		Strategy struct {
			FailFast *bool     `yaml:"fail-fast"`
			Matrix   yaml.Node `yaml:"matrix"`
		} `yaml:"strategy"`
		Steps []legStep `yaml:"steps"`
	} `yaml:"jobs"`
}

// legJobProblems checks one sharded job in body against the Makefile's legs.
func legJobProblems(body []byte, spec legJob, legs []string) []string {
	var wf legWorkflow
	if err := yaml.Unmarshal(body, &wf); err != nil {
		return []string{spec.workflow + ": not parseable YAML: " + err.Error()}
	}
	where := spec.workflow + "'s " + spec.job
	job, ok := wf.Jobs[spec.job]
	if !ok {
		return []string{spec.workflow + " has no " + spec.job + " job"}
	}
	var problems []string
	if job.Strategy.FailFast == nil || *job.Strategy.FailFast {
		problems = append(problems, where+" lacks `fail-fast: false`: one failing leg would cancel the others")
	}
	problems = append(problems, legMatrixProblems(where, spec.printTarget, job.Strategy.Matrix, legs)...)
	minutes, err := strconv.Atoi(job.Timeout)
	if err != nil || minutes <= legBudgetMargin || minutes > jobMinutesCeiling {
		problems = append(problems, where+"'s timeout-minutes "+strconv.Quote(job.Timeout)+
			" is not a literal number of minutes over "+strconv.Itoa(legBudgetMargin)+" and at most "+strconv.Itoa(jobMinutesCeiling))
	}
	budget := "GOTEST_TIMEOUT=" + strconv.Itoa(minutes-legBudgetMargin) + "m"
	runs := 0
	for _, s := range job.Steps {
		if !strings.Contains(s.Run, spec.target) {
			continue
		}
		runs++
		if s.Env[spec.legVar] != legMatrixEnv || !strings.Contains(s.Run, "$"+spec.legVar) {
			problems = append(problems, where+"'s step "+strconv.Quote(s.Name)+" does not run the matrix's leg; want env "+
				spec.legVar+": "+legMatrixEnv+" passed as $"+spec.legVar)
		}
		if err == nil && !strings.Contains(s.Run, budget) {
			problems = append(problems, where+"'s step "+strconv.Quote(s.Name)+" does not set "+budget+
				", go test's per-package budget 5 minutes inside the leg's limit")
		}
		if spec.copyOut && s.Env["IN_IMAGE_COPY_OUT"] != coverProfile {
			problems = append(problems, where+"'s step "+strconv.Quote(s.Name)+" does not copy "+coverProfile+
				" out of the stage's container (IN_IMAGE_COPY_OUT)")
		}
	}
	if runs != 1 {
		problems = append(problems, where+" has "+strconv.Itoa(runs)+" steps running "+strconv.Quote(spec.target)+"; want exactly one")
	}
	if spec.profile && !slices.ContainsFunc(job.Steps, func(s legStep) bool {
		return strings.HasPrefix(s.Uses, "actions/upload-artifact@") && s.With["name"] == coverProfileArt &&
			s.With["path"] == coverProfile && s.With["if-no-files-found"] == "error"
	}) {
		problems = append(problems, where+" does not upload "+coverProfile+" as "+coverProfileArt+
			" with if-no-files-found: error; the merge job would gate on a missing leg")
	}
	return problems
}

// legMatrixProblems checks the matrix is one literal `leg:` list naming every
// Makefile leg once.
func legMatrixProblems(where, printTarget string, m yaml.Node, legs []string) []string {
	if m.Kind != yaml.MappingNode || len(m.Content) != 2 || m.Content[0].Value != "leg" || m.Content[1].Kind != yaml.SequenceNode {
		return []string{where + "'s matrix is not one literal `leg:` list: any other axis multiplies or edits the legs"}
	}
	var problems, got []string
	for _, e := range m.Content[1].Content {
		switch {
		case slices.Contains(got, e.Value):
			problems = append(problems, where+"'s matrix names leg "+strconv.Quote(e.Value)+" twice")
		case !slices.Contains(legs, e.Value):
			problems = append(problems, where+"'s matrix names leg "+strconv.Quote(e.Value)+
				", which `make -s "+printTarget+"` does not print; fix: delete it from the matrix")
		}
		got = append(got, e.Value)
	}
	for _, l := range legs {
		if !slices.Contains(got, l) {
			problems = append(problems, where+"'s matrix runs no leg "+strconv.Quote(l)+
				", which `make -s "+printTarget+"` prints; fix: add it to the matrix")
		}
	}
	return problems
}

// mergeJobProblems checks the coverage gate over the legs.
func mergeJobProblems(body []byte, spec mergeJob) []string {
	var wf legWorkflow
	if err := yaml.Unmarshal(body, &wf); err != nil {
		return []string{spec.workflow + ": not parseable YAML: " + err.Error()}
	}
	where := spec.workflow + "'s " + spec.job
	job, ok := wf.Jobs[spec.job]
	if !ok {
		return []string{spec.workflow + " has no " + spec.job + " job, the one coverage gate over the legs"}
	}
	var problems []string
	if !slices.Contains(needsList(job.Needs), spec.legs) {
		problems = append(problems, where+" does not need "+spec.legs+": it would gate before the legs finish")
	}
	if !slices.ContainsFunc(job.Steps, func(s legStep) bool {
		return strings.HasPrefix(s.Uses, "actions/download-artifact@") && s.With["pattern"] == "coverage-leg-*" &&
			s.With["merge-multiple"] == "true" && s.With["path"] == "coverage"
	}) {
		problems = append(problems, where+" does not download every coverage-leg-* profile into coverage/")
	}
	if !slices.ContainsFunc(job.Steps, func(s legStep) bool { return strings.Contains(s.Run, "make coverage-merge") }) {
		problems = append(problems, where+" does not run `make coverage-merge`, the threshold and the baseline")
	}
	return problems
}

func TestCoverageLegs_MatchTheMakefile(t *testing.T) {
	root := repoRoot(t)
	files := workflowFiles(t)
	for _, spec := range legJobs {
		legs := strings.Fields(makeOutput(t, root, spec.printTarget))
		floor := coverLegFloor
		if spec.job == "test-sdk" {
			floor = sdkLegFloor
		}
		if len(legs) < floor {
			t.Fatalf("`make -s %s` printed %d legs, under the floor of %d", spec.printTarget, len(legs), floor)
		}
		for _, p := range legJobProblems(files[spec.workflow], spec, legs) {
			t.Error(p)
		}
	}
	for _, spec := range mergeJobs {
		for _, p := range mergeJobProblems(files[spec.workflow], spec) {
			t.Error(p)
		}
	}
}

func TestCoverageLegs_ListsAreTheUnitLegs(t *testing.T) {
	root := repoRoot(t)
	cover := makeOutput(t, root, "print-coverage-legs")
	if fips := makeOutput(t, root, "print-fips-legs"); cover != fips {
		t.Errorf("COVER_LEGS is %q and FIPS_LEGS %q; both are UNIT_LEGS", cover, fips)
	}
	sdk := strings.Fields(makeOutput(t, root, "print-test-sdk-legs"))
	for _, l := range sdk {
		if !slices.Contains(strings.Fields(cover), l) {
			t.Errorf("TEST_SDK_LEGS names %q, which is no unit leg", l)
		}
	}
}

const legFixture = `jobs:
  coverage:
    timeout-minutes: 180
    strategy:
      fail-fast: false
      matrix:
        leg: [a, b]
    steps:
      - name: leg
        env:
          COVER_LEG: ${{ matrix.leg }}
          IN_IMAGE_COPY_OUT: coverage/leg-${{ matrix.leg }}.out
        run: make coverage-leg COVER_LEG='$COVER_LEG' GOTEST_TIMEOUT=175m
      - uses: actions/upload-artifact@0000000000000000000000000000000000000000 # v6
        with:
          name: coverage-leg-${{ matrix.leg }}
          path: coverage/leg-${{ matrix.leg }}.out
          if-no-files-found: error
  coverage-merge:
    needs: [changes, coverage]
    steps:
      - uses: actions/download-artifact@0000000000000000000000000000000000000000 # v7
        with:
          pattern: coverage-leg-*
          merge-multiple: true
          path: coverage
      - run: make coverage-merge
`

var fixtureLegSpec = legJob{"ci.yml", "coverage", "print-coverage-legs", "coverage-leg COVER_LEG=", "COVER_LEG", true, true}

func TestCoverageLegs_FixtureIsValid(t *testing.T) {
	if p := legJobProblems([]byte(legFixture), fixtureLegSpec, []string{"a", "b"}); len(p) != 0 {
		t.Fatalf("the fixture's legs must pass: %v", p)
	}
	if p := mergeJobProblems([]byte(legFixture), mergeJob{"ci.yml", "coverage-merge", "coverage"}); len(p) != 0 {
		t.Fatalf("the fixture's merge must pass: %v", p)
	}
}

// TestCoverageLegs_CanFail mutates the fixture once per rule.
func TestCoverageLegs_CanFail(t *testing.T) {
	cases := map[string]struct{ old, new, want string }{
		"a missing leg":              {"leg: [a, b]", "leg: [a]", "runs no leg \"b\""},
		"an unknown leg":             {"leg: [a, b]", "leg: [a, b, c]", "does not print"},
		"a leg twice":                {"leg: [a, b]", "leg: [a, b, a]", "twice"},
		"a second axis":              {"leg: [a, b]", "leg: [a, b]\n        go: [x]", "not one literal `leg:` list"},
		"fail-fast":                  {"fail-fast: false", "fail-fast: true", "fail-fast: false"},
		"a limit over the ceiling":   {"timeout-minutes: 180", "timeout-minutes: 360", "at most 350"},
		"a budget past the limit":    {"GOTEST_TIMEOUT=175m", "GOTEST_TIMEOUT=180m", "GOTEST_TIMEOUT=175m"},
		"a step on a fixed leg":      {"COVER_LEG: ${{ matrix.leg }}", "COVER_LEG: a", "does not run the matrix's leg"},
		"no copy-out":                {"          IN_IMAGE_COPY_OUT: coverage/leg-${{ matrix.leg }}.out\n", "", "IN_IMAGE_COPY_OUT"},
		"an optional upload":         {"if-no-files-found: error", "if-no-files-found: warn", "does not upload"},
		"two leg steps":              {"      - uses: actions/upload-artifact", "      - run: make coverage-leg COVER_LEG=x\n      - uses: actions/upload-artifact", "want exactly one"},
		"merge without the legs":     {"needs: [changes, coverage]", "needs: [changes]", "does not need coverage"},
		"merge without the profiles": {"pattern: coverage-leg-*", "pattern: coverage-leg-a", "does not download"},
		"merge without the gate":     {"run: make coverage-merge", "run: make coverage", "does not run `make coverage-merge`"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(legFixture, c.old) {
				t.Fatalf("the fixture lacks %q", c.old)
			}
			body := []byte(strings.Replace(legFixture, c.old, c.new, 1))
			problems := append(legJobProblems(body, fixtureLegSpec, []string{"a", "b"}),
				mergeJobProblems(body, mergeJob{"ci.yml", "coverage-merge", "coverage"})...)
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, c.want) }) {
				t.Fatalf("want a problem containing %q, got %v", c.want, problems)
			}
		})
	}
}
