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
)

// Ruling 886: a pool build fetches its Alpine packages through the LAN apk
// cache (DEVCACHE_APK_MIRROR on the pool, APK_MIRROR in the build), a hosted
// build from dl-cdn.alpinelinux.org as before, and no image keeps the
// mirror's address. scripts/apk-mirror.sh does the switch; every apk call of
// the devcontainer Dockerfile goes through it.

const (
	apkMirrorScript = "scripts/apk-mirror.sh"
	apkMirrorMount  = "--mount=type=bind,source=" + apkMirrorScript + ",target=/usr/local/bin/apk-mirror"
	apkRepos        = "https://dl-cdn.alpinelinux.org/alpine/v3.22/main\nhttps://dl-cdn.alpinelinux.org/alpine/v3.22/community\n"
)

// fakeApk records the repositories file as apk saw it, and its arguments,
// and exits with FAKE_APK_STATUS.
const fakeApk = `#!/bin/sh
cat "$APK_REPOSITORIES" > "$FAKE_DIR/seen"
printf '%s\n' "$*" > "$FAKE_DIR/args"
exit "${FAKE_APK_STATUS:-0}"
`

func TestApkMirrorScript(t *testing.T) {
	script, err := filepath.Abs(filepath.Join(repoRoot(t), apkMirrorScript))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, mirror, status string
		wantCode             int
		wantSeen             string
		apkRuns              bool
	}{
		{name: "the pool's mirror", mirror: "http://cache.example:3143/alpine", wantSeen: "http://cache.example:3143/alpine/v3.22/main\nhttp://cache.example:3143/alpine/v3.22/community\n", apkRuns: true},
		{name: "a trailing slash", mirror: "http://cache.example:3143/alpine/", wantSeen: "http://cache.example:3143/alpine/v3.22/main\nhttp://cache.example:3143/alpine/v3.22/community\n", apkRuns: true},
		{name: "no mirror (a hosted runner)", mirror: "", wantSeen: apkRepos, apkRuns: true},
		{name: "a failed apk", mirror: "http://cache.example:3143/alpine", status: "3", wantCode: 3, wantSeen: "http://cache.example:3143/alpine/v3.22/main\nhttp://cache.example:3143/alpine/v3.22/community\n", apkRuns: true},
		{name: "a mirror that is no URL", mirror: "cache.example:3143", wantCode: 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "apk"), []byte(fakeApk), 0o755); err != nil { // #nosec G306 -- the stub must be executable
				t.Fatal(err)
			}
			repos := filepath.Join(dir, "repositories")
			if err := os.WriteFile(repos, []byte(apkRepos), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", script, "add", "--no-cache", "git") // #nosec G204 -- the repository's own script
			cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"APK_REPOSITORIES=" + repos, "APK_MIRROR=" + c.mirror, "FAKE_DIR=" + dir, "FAKE_APK_STATUS=" + c.status}
			out, err := cmd.CombinedOutput()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != c.wantCode {
				t.Fatalf("exit %d; want %d\n%s", code, c.wantCode, out)
			}
			seen, err := os.ReadFile(filepath.Join(dir, "seen"))
			if !c.apkRuns {
				if err == nil {
					t.Fatalf("apk ran with a refused mirror\n%s", out)
				}
			} else if string(seen) != c.wantSeen {
				t.Fatalf("apk saw\n%s\nwant\n%s", seen, c.wantSeen)
			}
			if c.apkRuns {
				if args, _ := os.ReadFile(filepath.Join(dir, "args")); strings.TrimSpace(string(args)) != "add --no-cache git" {
					t.Fatalf("apk got %q; want the caller's arguments", args)
				}
			}
			if after, _ := os.ReadFile(repos); string(after) != apkRepos {
				t.Fatalf("the repositories file was left as\n%s", after)
			}
		})
	}
}

var (
	apkCall   = regexp.MustCompile(`\bapk (add|upgrade|update)\b`)
	apkMirror = regexp.MustCompile(`\bapk-mirror (add|upgrade|update)\b`)
	stageLine = regexp.MustCompile(`(?m)^FROM\s`)
)

// devcontainerApkProblems lists every apk call of the devcontainer
// Dockerfile that would not go through the mirror switch.
func devcontainerApkProblems(dockerfile string) []string {
	var problems []string
	stages := stageLine.Split(dockerfile, -1)
	for i, stage := range stages[1:] {
		calls := apkMirror.FindAllString(stage, -1)
		if bare := apkCall.FindAllString(stage, -1); len(bare) > 0 {
			problems = append(problems, "stage "+firstLine(stage)+" runs "+strings.Join(bare, ", ")+" directly: want apk-mirror, so a pool build takes the LAN apk cache")
		}
		if len(calls) == 0 {
			continue
		}
		if !regexp.MustCompile(`(?m)^ARG APK_MIRROR\b`).MatchString(stage) {
			problems = append(problems, "stage "+firstLine(stage)+" calls apk-mirror without ARG APK_MIRROR: the build argument would not reach it")
		}
		for _, run := range regexp.MustCompile(`(?ms)^RUN .*?[^\\]$`).FindAllString(stage, -1) {
			if apkMirror.MatchString(run) && !strings.Contains(run, apkMirrorMount) {
				problems = append(problems, "stage "+firstLine(stage)+" runs apk-mirror without "+apkMirrorMount+": the script is bind-mounted, never copied into the image")
			}
		}
		_ = i
	}
	return problems
}

func TestDevcontainerApkGoesThroughTheMirror(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "build", "docker", "devcontainer", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !apkMirror.Match(data) {
		t.Fatal("the devcontainer Dockerfile calls apk-mirror nowhere: the parse found nothing to check")
	}
	for _, p := range devcontainerApkProblems(string(data)) {
		t.Error(p)
	}
	good := "FROM x AS a\nARG APK_MIRROR=\nRUN " + apkMirrorMount + " apk-mirror add --no-cache git\n"
	if p := devcontainerApkProblems(good); len(p) != 0 {
		t.Fatalf("a correct stage fails the check: %v", p)
	}
	for name, bad := range map[string]string{
		"a bare apk add":     "FROM x AS a\nARG APK_MIRROR=\nRUN apk add --no-cache git\n",
		"a bare apk upgrade": "FROM x AS a\nARG APK_MIRROR=\nRUN " + apkMirrorMount + " apk-mirror add git; apk upgrade\n",
		"no ARG":             "FROM x AS a\nRUN " + apkMirrorMount + " apk-mirror add --no-cache git\n",
		"no bind mount":      "FROM x AS a\nARG APK_MIRROR=\nRUN apk-mirror add --no-cache git\n",
	} {
		if p := devcontainerApkProblems(bad); len(p) == 0 {
			t.Errorf("%s passed the check", name)
		}
	}
}

// TestDevcontainerBuildPassesTheApkMirror: the build takes the pool's mirror
// as APK_MIRROR, takes none without it, and the toolchain tag hashes the
// script, since it shapes the image.
func TestDevcontainerBuildPassesTheApkMirror(t *testing.T) {
	root := repoRoot(t)
	run := func(extra ...string) string {
		args := append([]string{"-n", "-s", "docker-build-toolchain"}, extra...)
		cmd := exec.Command("make", args...) // #nosec G204 -- a fixed target
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "DEVCACHE_APK_MIRROR=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("make -n docker-build-toolchain: %v\n%s", err, out)
		}
		return string(out)
	}
	if out := run("DEVCACHE_APK_MIRROR=http://cache.example:3143/alpine"); !strings.Contains(out, `--build-arg APK_MIRROR="http://cache.example:3143/alpine"`) {
		t.Errorf("with DEVCACHE_APK_MIRROR set, the build passes no APK_MIRROR:\n%s", out)
	}
	if out := run(); strings.Contains(out, "APK_MIRROR") {
		t.Errorf("with DEVCACHE_APK_MIRROR unset, the build still passes APK_MIRROR:\n%s", out)
	}
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(mk), "\n") {
		if strings.HasPrefix(l, "TOOLCHAIN_TAG ") && !strings.Contains(l, "$(APK_MIRROR_SCRIPT)") {
			t.Errorf("TOOLCHAIN_TAG does not hash $(APK_MIRROR_SCRIPT): a change to it would keep the tag of an image it no longer builds")
		}
	}
}
