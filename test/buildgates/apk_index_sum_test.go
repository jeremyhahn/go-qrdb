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
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// apkProbeDocker stands in for docker: it records its arguments and prints
// what the script's probe prints in an Alpine image, one repository and one
// index checksum, so the script runs to its sum without a daemon or network.
const apkProbeDocker = `#!/bin/sh
printf '%s\n' "$@" > "$STUB_DIR/args"
printf 'https://dl-cdn.alpinelinux.org/alpine/v3.24/main\n0123 APKINDEX.0123.tar.gz\n'
`

// apkCaller is the one way a ci.yml job reads the Alpine package index sum.
const apkCaller = "sum=$(make -s apk-index-sum)"

// pinnedGoImage is GO_IMAGE as scripts/tool-pins.sh, its one source, pins it.
func pinnedGoImage(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "tool-pins.sh"))
	if err != nil {
		t.Fatalf("read tool pins: %v", err)
	}
	m := regexp.MustCompile(`(?m)^GO_IMAGE=(\S+)$`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("scripts/tool-pins.sh pins no GO_IMAGE")
	}
	return string(m[1])
}

// runApkProbe runs args in the repository root with the stub docker first on
// PATH and APK_BASE_IMAGES unset, as a CI step's shell has it, and returns
// the stub's directory, stdout, stderr and the run's error.
func runApkProbe(t *testing.T, root string, args ...string) (stub, stdout, stderr string, err error) {
	t.Helper()
	stub = t.TempDir()
	if werr := os.WriteFile(filepath.Join(stub, "docker"), []byte(apkProbeDocker), 0o700); werr != nil { // #nosec G306 -- the stub must be executable
		t.Fatalf("write stub docker: %v", werr)
	}
	cmd := exec.Command(args[0], args[1:]...) // #nosec G204 -- args are this file's own make target and script path
	cmd.Dir = root
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "APK_BASE_IMAGES=") && !strings.HasPrefix(kv, "APK_DOCKERFILE=") && !strings.HasPrefix(kv, "PATH=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"), "STUB_DIR="+stub)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err = cmd.Run()
	return stub, strings.TrimSpace(out.String()), errOut.String(), err
}

// TestApkIndexSum_ResolvesThePinnedGoImage runs the probe the way every
// ci.yml caller does, `make -s apk-index-sum`, against the repository's own
// Dockerfile, and requires it to read the index of the image
// scripts/tool-pins.sh pins (Codex P1 on #1296: the Dockerfile's FROM
// ${GO_IMAGE} has no ARG default, so the bare script failed every
// docker-build run).
func TestApkIndexSum_ResolvesThePinnedGoImage(t *testing.T) {
	root := repoRoot(t)
	stub, sum, stderr, err := runApkProbe(t, root, "make", "-s", "apk-index-sum")
	if err != nil {
		t.Fatalf("make -s apk-index-sum: %v\n%s", err, stderr)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sum) {
		t.Fatalf("make -s apk-index-sum printed %q; want only a sha256, since ci.yml puts all of stdout in APK_INDEX_SUM", sum)
	}
	args, err := os.ReadFile(filepath.Join(stub, "args"))
	if err != nil {
		t.Fatalf("the probe never ran docker: %v\n%s", err, stderr)
	}
	if want := pinnedGoImage(t, root); !slices.Contains(strings.Split(string(args), "\n"), want) {
		t.Fatalf("the probe ran docker on\n%s\nwant the pinned GO_IMAGE %s", args, want)
	}
}

// TestApkIndexSum_BareScriptCannotResolveTheDockerfile is the defect the make
// target exists for: the template script alone, with no APK_BASE_IMAGES,
// refuses this repository's Dockerfile before it runs anything. If it ever
// resolves, the Dockerfile has gained a GO_IMAGE default, a second copy of
// the pin, which scripts/tool-pins.sh is meant to be the only home of.
func TestApkIndexSum_BareScriptCannotResolveTheDockerfile(t *testing.T) {
	root := repoRoot(t)
	stub, _, stderr, err := runApkProbe(t, root, "scripts/apk-index-sum.sh")
	if err == nil {
		t.Fatalf("scripts/apk-index-sum.sh resolved the Dockerfile's FROM ${GO_IMAGE} on its own; a GO_IMAGE default there copies the pin out of scripts/tool-pins.sh")
	}
	if !strings.Contains(stderr, "has no global ARG default; set APK_BASE_IMAGES") {
		t.Fatalf("scripts/apk-index-sum.sh failed for another reason: %v\n%s", err, stderr)
	}
	if _, statErr := os.Stat(filepath.Join(stub, "args")); statErr == nil {
		t.Fatal("scripts/apk-index-sum.sh ran docker before it resolved the base image")
	}
}

// TestApkIndexSum_EveryCallerGoesThroughMake requires each ci.yml step that
// reads the index sum to take it from `make -s apk-index-sum`, never from the
// bare script, which cannot resolve the Dockerfile's base image.
func TestApkIndexSum_EveryCallerGoesThroughMake(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	ci := string(raw)
	if strings.Contains(ci, "$(scripts/apk-index-sum.sh") {
		t.Error("ci.yml runs scripts/apk-index-sum.sh directly; run `make -s apk-index-sum`, which supplies the GO_IMAGE pin")
	}
	// docker-build reads the sum, the one job that names and builds the
	// integration image; every other job loads that build from its artifact
	// (scripts/ci-image.sh, DS-7377) and names no image of its own.
	if got := strings.Count(ci, apkCaller); got != 1 {
		t.Errorf("ci.yml reads the sum through %q %d times; want exactly once, in docker-build", apkCaller, got)
	}
}
