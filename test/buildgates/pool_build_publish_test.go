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
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Codex P1 on #1631: a pool runner names the LAN CI registry in
// CI_IMAGE_REGISTRY, and `make docker-build` publishes every image it builds
// there through scripts/ci-image.sh registry-push. In pool-images that push
// would come before the image history check (ruling 888), so an image the
// check rejects would already sit in the registry under its content tag.
// pool-images therefore builds with CI_IMAGE_REGISTRY cleared for make alone;
// its own push step, after the check, is the only publication.

// poolMake is a make invocation that builds the devcontainer images, with the
// environment assignments before it.
var poolMake = regexp.MustCompile(`(?m)^\s*((?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*)make docker-build\b`)

// poolBuildPublishProblems lists each make docker-build in pool-images that
// keeps the runner's CI_IMAGE_REGISTRY.
func poolBuildPublishProblems(body []byte) ([]string, []string) {
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		return []string{"ci.yml does not parse: " + err.Error()}, nil
	}
	var problems, prefixes []string
	for _, s := range wf.Jobs["pool-images"].Steps {
		for _, m := range poolMake.FindAllStringSubmatch(s.Run, -1) {
			prefixes = append(prefixes, strings.TrimSpace(m[1]))
			if !strings.Contains(" "+m[1], " CI_IMAGE_REGISTRY= ") {
				problems = append(problems, "pool-images's step "+s.Name+" runs make docker-build with the runner's CI_IMAGE_REGISTRY: it publishes before the image history check; want CI_IMAGE_REGISTRY= make docker-build")
			}
		}
	}
	if len(prefixes) == 0 {
		problems = append(problems, "pool-images runs no make docker-build: the check found nothing to hold")
	}
	return problems, prefixes
}

func TestPoolImagesPublishOnlyAfterTheCheck(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	problems, prefixes := poolBuildPublishProblems(body)
	for _, p := range problems {
		t.Error(p)
	}
	const job = "jobs:\n  pool-images:\n    steps:\n      - name: Build\n        run: |\n          "
	if p, _ := poolBuildPublishProblems([]byte(job + `CI_IMAGE_REGISTRY= make docker-build MODULE_ENV_FILE="$F"` + "\n")); len(p) != 0 {
		t.Fatalf("a cleared registry fails: %v", p)
	}
	for name, bad := range map[string]string{
		"the runner's registry":    `make docker-build MODULE_ENV_FILE="$F"`,
		"the registry set again":   `CI_IMAGE_REGISTRY="$CI_REGISTRY" make docker-build`,
		"another variable cleared": `CI_REGISTRY= make docker-build`,
		"no build in pool-images":  `echo nothing`,
	} {
		if p, _ := poolBuildPublishProblems([]byte(job + bad + "\n")); len(p) == 0 {
			t.Errorf("%s passed", name)
		}
	}

	// The inherited environment: under the build line's own assignments, with
	// the pool's CI_IMAGE_REGISTRY inherited, ci-image.sh registry-push (what
	// docker-build-toolchain and docker-build-it call after a build) never
	// reaches docker.
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte("#!/bin/sh\necho \"docker $*\" >> \"$STUB_DIR/calls\"\nexit 0\n"), 0o700); err != nil { // #nosec G306 -- the stub must be executable
		t.Fatal(err)
	}
	for _, prefix := range prefixes {
		args := append(strings.Fields(prefix), filepath.Join(root, "scripts", "ci-image.sh"), "registry-push", "qrdb-toolchain:0123456789abcdef")
		cmd := exec.Command("env", args...) // #nosec G204 -- the workflow's own assignments and the repository's own script
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"), "STUB_DIR="+stub,
			"CI_IMAGE_REGISTRY=192.168.77.7:5005", "CI_IMAGE_DIGEST_DIR="+t.TempDir())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ci-image.sh registry-push under %q: %v\n%s", prefix, err, out)
		}
		if calls, _ := os.ReadFile(filepath.Join(stub, "calls")); len(calls) != 0 {
			t.Fatalf("under pool-images's build assignments %q, registry-push still calls docker with the inherited registry:\n%s", prefix, calls)
		}
	}
}
