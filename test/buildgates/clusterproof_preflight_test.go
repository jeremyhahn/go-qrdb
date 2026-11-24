package buildgates

// #1619 (PR #1637 review): preflight's clusterproof output selects the
// clusterproof-soak job on a pull request that changes the cluster proof.
// When the diff that finds the changed files fails, the step fails closed:
// it selects the soak and warns, rather than leaving clusterproof=false.
// These tests run the step's own script from ci.yml under bash, with a git
// stub on PATH, and read the output it writes to GITHUB_OUTPUT.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gocodecyaml "github.com/jeremyhahn/go-codec/pkg/yaml"
)

// preflightDiffStep is the id of the preflight step that sets clusterproof.
const preflightDiffStep = "diff"

type preflightWorkflow struct {
	Env  map[string]string `yaml:"env"`
	Jobs map[string]struct {
		Steps []struct {
			ID  string `yaml:"id"`
			Run string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// preflightScript returns the run script of preflight's diff step and the
// workflow-level env it reads.
func preflightScript(t *testing.T) (string, map[string]string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf preflightWorkflow
	if err := gocodecyaml.New[preflightWorkflow]().Unmarshal(b, &wf); err != nil {
		t.Fatal(err)
	}
	for _, s := range wf.Jobs["preflight"].Steps {
		if s.ID == preflightDiffStep {
			if s.Run == "" {
				t.Fatalf("preflight step %q has no run script", preflightDiffStep)
			}
			return s.Run, wf.Env
		}
	}
	t.Fatalf("preflight has no step with id %q", preflightDiffStep)
	return "", nil
}

// runPreflight runs the diff step with a git whose `diff` prints files and
// exits with diffRC, and returns the step's GITHUB_OUTPUT and stdout.
func runPreflight(t *testing.T, files string, diffRC string) (string, string) {
	t.Helper()
	script, wfEnv := preflightScript(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o750); err != nil {
		t.Fatal(err)
	}
	stub := "#!/usr/bin/env bash\nif [ \"$1\" = diff ]; then printf '%s' \"$STUB_FILES\"; exit \"$STUB_RC\"; fi\nexit 1\n"
	// #nosec G306 -- test stub must be executable
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "output")
	if err := os.WriteFile(out, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- fixed interpreter, script from the repo's workflow
	cmd := exec.Command("bash", "-e", "-c", script)
	cmd.Env = []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"GITHUB_OUTPUT=" + out,
		"EVENT=pull_request",
		"BASE=base",
		"HEAD=head",
		"CI_FULL_TIER=false",
		"DOCS_ONLY_PATTERN=" + wfEnv["DOCS_ONLY_PATTERN"],
		"CLUSTERPROOF_PATTERN=" + wfEnv["CLUSTERPROOF_PATTERN"],
		"STUB_FILES=" + files,
		"STUB_RC=" + diffRC,
	}
	stdout, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("preflight diff step: %v\n%s", err, stdout)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(got), string(stdout)
}

func TestClusterproofPreflightSelectsSoak(t *testing.T) {
	cases := map[string]struct {
		files, rc string
		want      string
		warn      bool
	}{
		"the script changed":  {files: "scripts/cluster-proof-ci.sh\nREADME.md\n", rc: "0", want: "clusterproof=true"},
		"the harness changed": {files: "test/clusterproof/soak/check.go\n", rc: "0", want: "clusterproof=true"},
		"neither changed":     {files: "internal/x/x.go\n", rc: "0", want: "clusterproof=false"},
		"the diff fails":      {files: "", rc: "128", want: "clusterproof=true", warn: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, stdout := runPreflight(t, c.files, c.rc)
			if !strings.Contains(got, c.want+"\n") {
				t.Errorf("GITHUB_OUTPUT = %q, want a line %q", got, c.want)
			}
			warned := strings.Contains(stdout, "::warning::") && strings.Contains(stdout, "git diff")
			if warned != c.warn {
				t.Errorf("warning naming the failed git diff = %v, want %v; stdout:\n%s", warned, c.warn, stdout)
			}
		})
	}
}
