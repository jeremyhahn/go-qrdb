// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package buildgates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A pool's Docker daemon runs outside the job container, so a path on the job's
// filesystem does not exist where the daemon resolves a bind mount (Codex P2
// on #1631). The Go caches under $RUNNER_TEMP/ci-cache, restored and saved by
// actions/cache and bind-mounted through CI_CACHE_DIR, are therefore the hosted
// runner's alone: on a pool runner scripts/in-toolchain-image.sh takes its
// daemon-local named volumes (<prefix>cache-gobuild, -golangci), kept across
// runs on that daemon, and refuses a CI_CACHE_DIR.

const hostedOnly = "runner.environment == 'github-hosted'"

// poolCacheProblems lists each step of a pool-routable job that restores,
// points at or saves the runner-local Go cache without being held to hosted
// runners.
func poolCacheProblems(body []byte) []string {
	var wf struct {
		Jobs map[string]struct {
			RunsOn yaml.Node `yaml:"runs-on"`
			Steps  []struct {
				Name string            `yaml:"name"`
				If   string            `yaml:"if"`
				Run  string            `yaml:"run"`
				With map[string]string `yaml:"with"`
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
		for _, s := range job.Steps {
			if !strings.Contains(s.With["path"], "ci-cache") && !strings.Contains(s.Run, "CI_CACHE_DIR") {
				continue
			}
			if !strings.Contains(s.If, hostedOnly) {
				problems = append(problems, name+": "+s.Name+" uses the runner-local cache on a pool runner, where the daemon cannot reach it; want if: "+hostedOnly)
			}
		}
	}
	return problems
}

func TestPoolJobsKeepTheGoCachesOnTheDaemon(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range poolCacheProblems(body) {
		t.Error(p)
	}
	const pool = "jobs:\n  a:\n    runs-on: ${{ cond && fromJSON('[\"self-hosted\",\"bigdisk-docker\"]') || 'ubuntu-latest' }}\n    steps:\n"
	good := pool +
		"      - name: Restore\n        if: ${{ " + hostedOnly + " }}\n        uses: actions/cache/restore@x\n        with:\n          path: ${{ runner.temp }}/ci-cache\n" +
		"      - name: Point\n        if: ${{ " + hostedOnly + " }}\n        run: echo \"CI_CACHE_DIR=$RUNNER_TEMP/ci-cache\" >> \"$GITHUB_ENV\"\n"
	if p := poolCacheProblems([]byte(good)); len(p) != 0 {
		t.Fatalf("a hosted-only cache fails: %v", p)
	}
	for name, bad := range map[string]string{
		"a restore on the pool": pool + "      - name: Restore\n        uses: actions/cache/restore@x\n        with:\n          path: ${{ runner.temp }}/ci-cache\n",
		"a pointer on the pool": pool + "      - name: Point\n        run: echo \"CI_CACHE_DIR=$RUNNER_TEMP/ci-cache\" >> \"$GITHUB_ENV\"\n",
		"a save on the pool":    pool + "      - name: Save\n        if: ${{ !cancelled() }}\n        uses: actions/cache/save@x\n        with:\n          path: ${{ runner.temp }}/ci-cache\n",
	} {
		if len(poolCacheProblems([]byte(bad))) == 0 {
			t.Errorf("%s passed", name)
		}
	}
	hosted := "jobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - name: Point\n        run: echo \"CI_CACHE_DIR=$RUNNER_TEMP/ci-cache\" >> \"$GITHUB_ENV\"\n"
	if p := poolCacheProblems([]byte(hosted)); len(p) != 0 {
		t.Fatalf("a hosted job was held to the pool rule: %v", p)
	}
}

// TestInToolchainImageRefusesARunnerPathOnThePool: on a self-hosted runner a
// CI_CACHE_DIR names a path the daemon cannot reach, so the script stops
// before it starts anything; on a hosted runner the same setting passes that
// check.
func TestInToolchainImageRefusesARunnerPathOnThePool(t *testing.T) {
	script := filepath.Join(repoRoot(t), "scripts", "in-toolchain-image.sh")
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte("#!/bin/sh\necho \"docker $*\" >> \"$STUB_DIR/calls\"\nexit 1\n"), 0o700); err != nil { // #nosec G306 -- the stub must be executable
		t.Fatal(err)
	}
	run := func(environment string) (string, error) {
		cmd := exec.Command("bash", script, "image:tag", "true") // #nosec G204 -- the repository's own script
		cmd.Env = append(os.Environ(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"), "STUB_DIR="+stub,
			"RUNNER_ENVIRONMENT="+environment, "CI_CACHE_DIR="+filepath.Join(stub, "ci-cache"), "CI_CACHE=on")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("self-hosted")
	if err == nil || !strings.Contains(out, "CI_CACHE_DIR") || !strings.Contains(out, "named volumes") {
		t.Fatalf("a CI_CACHE_DIR on a self-hosted runner is not refused (%v):\n%s", err, out)
	}
	if calls, _ := os.ReadFile(filepath.Join(stub, "calls")); len(calls) != 0 {
		t.Fatalf("the script ran docker before refusing the cache path:\n%s", calls)
	}
	if out, _ := run("github-hosted"); strings.Contains(out, "named volumes") {
		t.Fatalf("a CI_CACHE_DIR on a hosted runner is refused:\n%s", out)
	}
}

// TestInToolchainImageAcceptsTheSelectedPoolCache: on a self-hosted runner the
// launcher takes the shared generation scripts/ci-cache-dir.sh selects on the
// data array (Codex P1 on #1597), and still refuses any other path, including
// one under the same root.
func TestInToolchainImageAcceptsTheSelectedPoolCache(t *testing.T) {
	scripts := filepath.Join(repoRoot(t), "scripts")
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte("#!/bin/sh\necho \"docker $*\" >> \"$STUB_DIR/calls\"\nexit 1\n"), 0o700); err != nil { // #nosec G306 -- the stub must be executable
		t.Fatal(err)
	}
	root := filepath.Join(stub, "raid")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"), "STUB_DIR="+stub,
		"RUNNER_ENVIRONMENT=self-hosted", "RUNNER_TEMP=", "CI_CACHE_ROOT="+root, "CI_CACHE=on")
	sel := exec.Command("bash", filepath.Join(scripts, "ci-cache-dir.sh")) // #nosec G204 -- the repository's own script
	sel.Env = env
	line, err := sel.Output()
	if err != nil {
		t.Fatalf("ci-cache-dir.sh: %v", err)
	}
	dir := strings.TrimPrefix(strings.TrimSpace(string(line)), "CI_CACHE_DIR=")
	if !strings.HasPrefix(dir, root+"/") {
		t.Fatalf("ci-cache-dir.sh did not select a directory under %s: %q", root, line)
	}
	run := func(cacheDir string) (string, error) {
		cmd := exec.Command("bash", filepath.Join(scripts, "in-toolchain-image.sh"), "image:tag", "true") // #nosec G204 -- the repository's own script
		cmd.Env = append(env, "CI_CACHE_DIR="+cacheDir)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, _ := run(dir); strings.Contains(out, "named volumes") {
		t.Fatalf("the generation ci-cache-dir.sh selected (%s) is refused:\n%s", dir, out)
	}
	other := filepath.Join(root, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{other, filepath.Join(stub, "ci-cache"), filepath.Dir(dir), dir + "/../" + filepath.Base(dir)} {
		if out, err := run(bad); err == nil || !strings.Contains(out, "named volumes") {
			t.Errorf("CI_CACHE_DIR=%s on a self-hosted runner is not refused (%v):\n%s", bad, err, out)
		}
	}
}
