package buildgates

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Ruling 887: internal addresses stay out of public repositories, and their
// CI logs count. A pool runner's environment names LAN addresses (the
// DEVCACHE_* caches, the GOPROXY module proxy, the LAN CI registry); a
// BuildKit build echoes its build arguments and `go env` prints GOPROXY.
// Every pool job therefore runs scripts/ci-mask-lan.sh before any step that
// builds, and the runner masks each address in every later log line.

const (
	maskLANScript = "scripts/ci-mask-lan.sh"
	maskLANRoot   = "../.."
)

// maskLANBuildStep is a step that builds or runs anything that could print
// the environment: make, docker, go, or the image wrappers.
var maskLANBuildStep = regexp.MustCompile(`(^|[\s;&|(` + "`" + `])(make|docker|go|scripts/in-toolchain-image\.sh|scripts/ci-image\.sh)(\s|$)`)

// maskLANPoolLabel names a runs-on that can reach the pools.
var maskLANPoolLabel = regexp.MustCompile(`self-hosted|bigdisk|codex|qwen`)

// maskLANProblems lists each pool job that builds without the mask step
// before its first build step. A pool job that builds nothing (one that only
// reads the other jobs' results, like a gate) prints no environment and is
// left alone.
func maskLANProblems(body []byte) []string {
	var wf struct {
		Jobs map[string]struct {
			RunsOn yaml.Node `yaml:"runs-on"`
			Steps  []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		return []string{"ci.yml does not parse: " + err.Error()}
	}
	var problems []string
	for name, job := range wf.Jobs {
		var rb strings.Builder
		var walk func(*yaml.Node)
		walk = func(n *yaml.Node) {
			rb.WriteString(n.Value + " ")
			for _, c := range n.Content {
				walk(c)
			}
		}
		walk(&job.RunsOn)
		if !maskLANPoolLabel.MatchString(rb.String()) {
			continue
		}
		mask, build := -1, -1
		for i, s := range job.Steps {
			if mask < 0 && strings.TrimSpace(s.Run) == maskLANScript {
				mask = i
			}
			if build < 0 && maskLANBuildStep.MatchString(s.Run) {
				build = i
			}
		}
		switch {
		case build < 0:
		case mask < 0:
			problems = append(problems, "pool job "+name+" never runs "+maskLANScript+": its log could print a LAN address (ruling 887)")
		case build < mask:
			problems = append(problems, "pool job "+name+" runs "+strings.TrimSpace(job.Steps[build].Name)+" before "+maskLANScript)
		}
	}
	slices.Sort(problems)
	return problems
}

func TestCIPoolJobsMaskTheLANAddresses(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(maskLANRoot, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range maskLANProblems(body) {
		t.Error(p)
	}
	const pool = "jobs:\n  a:\n    runs-on: [self-hosted, bigdisk-docker]\n    steps:\n"
	if p := maskLANProblems([]byte(pool + "      - run: " + maskLANScript + "\n      - run: make build\n")); len(p) != 0 {
		t.Fatalf("a masked pool job fails: %v", p)
	}
	for name, body := range map[string]string{
		"no mask step":         pool + "      - run: make build\n",
		"the mask after build": pool + "      - name: Build\n        run: docker build .\n      - run: " + maskLANScript + "\n",
	} {
		if len(maskLANProblems([]byte(body))) == 0 {
			t.Errorf("%s passed", name)
		}
	}
	if p := maskLANProblems([]byte("jobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make build\n")); len(p) != 0 {
		t.Fatalf("a hosted job was held to the pool rule: %v", p)
	}
	if p := maskLANProblems([]byte(pool + "      - run: echo \"$RESULTS\"\n")); len(p) != 0 {
		t.Fatalf("a pool job that builds nothing was held to the rule: %v", p)
	}
}

func TestCIMaskLANScript(t *testing.T) {
	script, err := filepath.Abs(filepath.Join(maskLANRoot, maskLANScript))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script) // #nosec G204 -- the repository's own script
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"DEVCACHE_APT_PROXY=http://192.168.101.12:3142",
		"DEVCACHE_APK_MIRROR=http://192.168.101.12:3143/alpine",
		"GOPROXY=http://10.1.2.3:3300|https://proxy.golang.org,direct",
		"CI_REGISTRY=172.20.0.5:5005",
		"OTHER=http://192.168.9.9:1",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", maskLANScript, err, out)
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, want := range []string{
		"::add-mask::http://192.168.101.12:3142", "::add-mask::192.168.101.12:3142", "::add-mask::192.168.101.12",
		"::add-mask::http://192.168.101.12:3143/alpine", "::add-mask::192.168.101.12:3143",
		"::add-mask::http://10.1.2.3:3300", "::add-mask::10.1.2.3",
		"::add-mask::172.20.0.5:5005", "::add-mask::172.20.0.5",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	for _, l := range got {
		if strings.Contains(l, "proxy.golang.org") || strings.Contains(l, "direct") || strings.Contains(l, "192.168.9.9") {
			t.Errorf("masked %q: a public host, a keyword or a variable outside the list", l)
		}
	}
}
