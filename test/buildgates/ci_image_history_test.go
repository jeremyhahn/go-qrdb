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

// Ruling 888: a pool build takes the LAN caches as BuildKit secrets, never as
// build arguments, whose values BuildKit records in the image history. A
// pool-built image may one day be pushed beyond the LAN, and ruling 887 covers
// its metadata. scripts/ci-image-history.sh reads an image's history and
// config and fails on any private (RFC 1918) address; it names the image and
// the history entry, never the address.

const imageHistoryScript = "scripts/ci-image-history.sh"

// fakeHistoryDocker answers `docker history` with $STUB_DIR/<n>.history and
// `docker image inspect` with $STUB_DIR/<n>.config, n being the image name
// with ":" and "/" turned to "_". A missing file is a docker failure.
const fakeHistoryDocker = `#!/bin/sh
for a; do image=$a; done
n=$(printf '%s' "$image" | tr ':/' '__')
case "$1 $2" in
"history --no-trunc") f="$STUB_DIR/$n.history" ;;
"image inspect") f="$STUB_DIR/$n.config" ;;
*) echo "stub docker: unexpected $*" >&2; exit 99 ;;
esac
[ -f "$f" ] || { echo "Error: No such image: $image" >&2; exit 1; }
cat "$f"
`

const cleanHistory = `RUN |0 /bin/sh -c apk-mirror add --no-cache git # buildkit
RUN /bin/sh -c go install golang.org/x/tools@v0.30.1 # go1.27.4, libfoo 1.10.2.3
ENV PATH=/usr/local/go/bin:/usr/bin
ADD alpine-minirootfs-3.22.1-x86_64.tar.gz / # buildkit`

const cleanConfig = `{"Env":["PATH=/usr/bin","LISTEN=127.0.0.1:8080","PEER=172.32.0.5"],"Labels":{"v":"10.2"}}`

func runImageHistory(t *testing.T, files map[string]string, images ...string) (string, error) {
	t.Helper()
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte(fakeHistoryDocker), 0o700); err != nil { // #nosec G306 -- the stub must be executable
		t.Fatalf("write stub docker: %v", err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(stub, name), []byte(body+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	cmd := exec.Command(filepath.Join(repoRoot(t), imageHistoryScript), images...) // #nosec G204 -- the repository's own script
	cmd.Env = append(os.Environ(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"), "STUB_DIR="+stub)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCIImageHistoryScript(t *testing.T) {
	clean := map[string]string{"a_1.history": cleanHistory, "a_1.config": cleanConfig}
	if out, err := runImageHistory(t, clean, "a:1"); err != nil {
		t.Fatalf("a clean image fails: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name, history, config, want string
	}{
		{name: "an apk mirror build argument", history: "RUN |1 APK_MIRROR=http://192.168.77.7:3143/alpine /bin/sh -c apk-mirror add git", config: cleanConfig, want: "history entry 1"},
		{name: "an apt proxy in a later entry", history: cleanHistory + "\nRUN |1 APT_PROXY=http://10.20.30.40:3142 /bin/sh -c apt-get update", config: cleanConfig, want: "history entry 5"},
		{name: "a 172.16/12 address", history: "ENV REG=172.20.0.5:5005", config: cleanConfig, want: "history entry 1"},
		{name: "an address at the start of an entry", history: "192.168.1.1", config: cleanConfig, want: "history entry 1"},
		{name: "an address in the config", history: cleanHistory, config: `{"Env":["PEER_URL=http://10.0.0.7:3000"]}`, want: "config"},
		{name: "an address in a label", history: cleanHistory, config: `{"Labels":{"cache":"192.168.77.7"}}`, want: "config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runImageHistory(t, map[string]string{"a_1.history": tc.history, "a_1.config": tc.config}, "a:1")
			if err == nil {
				t.Fatalf("an image carrying a private address passes:\n%s", out)
			}
			if !strings.Contains(out, "a:1") || !strings.Contains(out, tc.want) {
				t.Fatalf("the failure does not name the image and %q:\n%s", tc.want, out)
			}
			for _, addr := range []string{"192.168.", "10.20.30.40", "10.0.0.7", "172.20.0.5"} {
				if strings.Contains(out, addr) {
					t.Fatalf("the failure prints the address %s it should keep out of the log:\n%s", addr, out)
				}
			}
		})
	}
	t.Run("every image is read", func(t *testing.T) {
		files := map[string]string{"a_1.history": cleanHistory, "a_1.config": cleanConfig,
			"b_2.history": "RUN |1 APK_MIRROR=http://192.168.77.7:3143/alpine x", "b_2.config": cleanConfig}
		if out, err := runImageHistory(t, files, "a:1", "b:2"); err == nil || !strings.Contains(out, "b:2") {
			t.Fatalf("a private address in the second image passes (%v):\n%s", err, out)
		}
	})
	t.Run("an unreadable image fails closed", func(t *testing.T) {
		if out, err := runImageHistory(t, clean, "a:1", "missing:1"); err == nil || !strings.Contains(out, "missing:1") {
			t.Fatalf("an image docker cannot read passes (%v):\n%s", err, out)
		}
	})
	t.Run("no image is a usage error", func(t *testing.T) {
		if out, err := runImageHistory(t, clean); err == nil || !strings.Contains(out, "usage") {
			t.Fatalf("no image passes (%v):\n%s", err, out)
		}
	})
}

// poolHistoryProblems: pool-images checks both images it built with
// scripts/ci-image-history.sh, by the IDs the build step read, under the build
// step's condition, after the build and before the push to the LAN registry,
// so an image carrying a private address is never pushed or run by a leg.
func poolHistoryProblems(body []byte) []string {
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				ID  string            `yaml:"id"`
				If  string            `yaml:"if"`
				Env map[string]string `yaml:"env"`
				Run string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		return []string{"ci.yml does not parse: " + err.Error()}
	}
	job, ok := wf.Jobs["pool-images"]
	if !ok {
		return []string{"ci.yml has no pool-images job"}
	}
	build, check, push := -1, -1, -1
	for i, s := range job.Steps {
		switch {
		case s.ID == "build":
			build = i
		case s.ID == "registry":
			push = i
		case strings.Contains(s.Run, imageHistoryScript):
			check = i
		}
	}
	switch {
	case build < 0 || push < 0:
		return []string{"pool-images has no build step (id build) or no push step (id registry)"}
	case check < 0:
		return []string{"pool-images never runs " + imageHistoryScript + " on the images it built"}
	case check < build || check > push:
		return []string{"pool-images runs " + imageHistoryScript + " outside the build and the push: it must check the built images before they are pushed"}
	}
	s := job.Steps[check]
	var problems []string
	if s.If != job.Steps[build].If {
		problems = append(problems, "the history check runs under "+s.If+", not the build step's condition "+job.Steps[build].If)
	}
	for name, out := range map[string]string{"TOOLCHAIN_ID": "toolchain_id", "IT_ID": "it_id"} {
		if s.Env[name] != "${{ steps.build.outputs."+out+" }}" || !strings.Contains(s.Run, `"$`+name+`"`) {
			problems = append(problems, "the history check does not read the built image $"+name+" (steps.build.outputs."+out+")")
		}
	}
	return problems
}

func TestPoolImagesCheckTheirHistory(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range poolHistoryProblems(body) {
		t.Error(p)
	}
	const cond = `${{ runner.environment != 'github-hosted' }}`
	step := func(id, run string) string {
		return "      - id: " + id + "\n        if: " + cond + "\n        env:\n          TOOLCHAIN_ID: ${{ steps.build.outputs.toolchain_id }}\n          IT_ID: ${{ steps.build.outputs.it_id }}\n        run: " + run + "\n"
	}
	wf := func(steps ...string) []byte {
		return []byte("jobs:\n  pool-images:\n    steps:\n" + strings.Join(steps, ""))
	}
	buildStep, pushStep := step("build", "make docker-build"), step("registry", "docker push x")
	checkStep := step("history", imageHistoryScript+` "$TOOLCHAIN_ID" "$IT_ID"`)
	if p := poolHistoryProblems(wf(buildStep, checkStep, pushStep)); len(p) != 0 {
		t.Fatalf("a correct pool-images fails: %v", p)
	}
	for name, bad := range map[string][]byte{
		"no check":                   wf(buildStep, pushStep),
		"the check after the push":   wf(buildStep, pushStep, checkStep),
		"the check before the build": wf(checkStep, buildStep, pushStep),
		"one image unchecked":        wf(buildStep, step("history", imageHistoryScript+` "$TOOLCHAIN_ID"`), pushStep),
		"another condition":          wf(buildStep, strings.Replace(checkStep, cond, "${{ always() }}", 1), pushStep),
	} {
		if len(poolHistoryProblems(bad)) == 0 {
			t.Errorf("%s passed", name)
		}
	}
}
