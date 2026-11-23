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
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	devcontainerCompose     = "build/docker/devcontainer/compose.yaml"
	devcontainerPinsOverlay = "build/docker/devcontainer/compose.pins.yaml"
)

// devcontainerPinFiles are the files the VS Code initializeCommand path needs,
// relative to the repository root.
var devcontainerPinFiles = []string{
	".devcontainer.json",
	"scripts/devcontainer-pins.sh",
	"scripts/tool-pins.sh",
	devcontainerCompose,
}

// devcontainerArgs decodes services.devcontainer.build.args of a compose file;
// yaml.v3 resolves the `<<` merge keys, and an empty passthrough arg decodes
// as nil (returned as "").
func devcontainerArgs(raw []byte) (map[string]string, error) {
	var doc struct {
		Services map[string]struct {
			Build struct {
				Args map[string]any `yaml:"args"`
			} `yaml:"build"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range doc.Services["devcontainer"].Build.Args {
		if v == nil {
			out[k] = ""
			continue
		}
		out[k] = fmt.Sprint(v)
	}
	return out, nil
}

// devcontainerPinsDefects runs .devcontainer.json's initializeCommand in root
// with env and checks that the compose files VS Code merges hand the
// devcontainer build the GO_IMAGE and TRIVY_IMAGE that scripts/tool-pins.sh
// pins. Compose merges build.args per key with the later file winning, so the
// merged value is the base compose.yaml arg overridden by the overlay's.
func devcontainerPinsDefects(root string, env []string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(root, ".devcontainer.json"))
	if err != nil {
		return nil, err
	}
	var dc struct {
		InitializeCommand string   `json:"initializeCommand"`
		DockerComposeFile []string `json:"dockerComposeFile"`
	}
	if err := json.Unmarshal(stripJSONComments(raw), &dc); err != nil {
		return nil, fmt.Errorf(".devcontainer.json: %w", err)
	}
	var defects []string
	if strings.TrimSpace(dc.InitializeCommand) == "" {
		defects = append(defects, ".devcontainer.json has no initializeCommand: nothing writes "+devcontainerPinsOverlay+" before VS Code builds")
	}
	base := slices.Index(dc.DockerComposeFile, devcontainerCompose)
	overlay := slices.Index(dc.DockerComposeFile, devcontainerPinsOverlay)
	if base < 0 || overlay < 0 || overlay < base {
		defects = append(defects, fmt.Sprintf(".devcontainer.json dockerComposeFile is %q, want %q after %q", dc.DockerComposeFile, devcontainerPinsOverlay, devcontainerCompose))
	}

	pins, err := os.ReadFile(filepath.Join(root, "scripts", "tool-pins.sh"))
	if err != nil {
		return nil, err
	}
	want := map[string]string{}
	for _, key := range []string{"GO_IMAGE", "TRIVY_IMAGE"} {
		m := regexp.MustCompile(`(?m)^` + key + `=(\S+)$`).FindSubmatch(pins)
		if m == nil {
			return nil, fmt.Errorf("scripts/tool-pins.sh pins no %s", key)
		}
		want[key] = string(m[1])
	}

	overlayPath := filepath.Join(root, filepath.FromSlash(devcontainerPinsOverlay))
	if err := os.Remove(overlayPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	merged := map[string]string{}
	composeRaw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(devcontainerCompose)))
	if err != nil {
		return nil, err
	}
	baseArgs, err := devcontainerArgs(composeRaw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", devcontainerCompose, err)
	}
	for k, v := range baseArgs {
		merged[k] = v
	}
	if strings.TrimSpace(dc.InitializeCommand) != "" {
		cmd := exec.Command("sh", "-c", dc.InitializeCommand) // #nosec G204 -- the command is .devcontainer.json's own initializeCommand, run in this test's temp copy
		cmd.Dir = root
		cmd.Env = env
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			defects = append(defects, fmt.Sprintf(".devcontainer.json initializeCommand %q failed: %v: %s", dc.InitializeCommand, err, strings.TrimSpace(stderr.String())))
		}
	}
	overlayRaw, err := os.ReadFile(overlayPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		defects = append(defects, devcontainerPinsOverlay+" was not written by the initializeCommand")
	case err != nil:
		return nil, err
	default:
		overlayArgs, err := devcontainerArgs(overlayRaw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", devcontainerPinsOverlay, err)
		}
		for k, v := range overlayArgs {
			merged[k] = v
		}
	}
	for _, key := range []string{"GO_IMAGE", "TRIVY_IMAGE"} {
		if got := merged[key]; got != want[key] {
			defects = append(defects, fmt.Sprintf("merged %s + %s services.devcontainer.build.args %s is %q, want %q from scripts/tool-pins.sh", devcontainerCompose, devcontainerPinsOverlay, key, got, want[key]))
		}
	}
	return defects, nil
}

// copyDevcontainerPinFiles copies devcontainerPinFiles from the repository
// into a fresh temp root at the same relative paths.
func copyDevcontainerPinFiles(t *testing.T) string {
	t.Helper()
	src := repoRoot(t)
	dst := t.TempDir()
	for _, rel := range devcontainerPinFiles {
		raw, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		out := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		// #nosec G703 -- out is filepath.Join(dst, rel): dst is the test's t.TempDir() root and rel comes from the literal devcontainerPinFiles list, so no input outside the test chooses the path.
		if err := os.WriteFile(out, raw, 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return dst
}

// envWithoutImagePins is the test's environment minus any exported GO_IMAGE
// or TRIVY_IMAGE, so the overlay is the only source of the pins.
func envWithoutImagePins() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GO_IMAGE=") || strings.HasPrefix(kv, "TRIVY_IMAGE=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func TestDevcontainerInitializerPinsTheImages(t *testing.T) {
	root := copyDevcontainerPinFiles(t)
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(devcontainerPinsOverlay))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s exists before the initializeCommand runs (stat err %v)", devcontainerPinsOverlay, err)
	}
	defects, err := devcontainerPinsDefects(root, envWithoutImagePins())
	if err != nil {
		t.Fatalf(".devcontainer.json: %v", err)
	}
	for _, d := range defects {
		t.Errorf(".devcontainer.json: %s", d)
	}
}

func TestDevcontainerPinsDefectsFixtures(t *testing.T) {
	cases := map[string]struct {
		file, old, new string
		wantDefect     bool
	}{
		"no initializeCommand": {".devcontainer.json", "\t\"initializeCommand\": \"sh scripts/devcontainer-pins.sh\",\n", "", true},
		"overlay not in dockerComposeFile": {".devcontainer.json",
			`, "build/docker/devcontainer/compose.pins.yaml"`, "", true},
		"script drops TRIVY_IMAGE": {"scripts/devcontainer-pins.sh", "        TRIVY_IMAGE: \"${TRIVY_IMAGE}\"\n", "", true},
		"script drops GO_IMAGE":    {"scripts/devcontainer-pins.sh", "        GO_IMAGE: \"${GO_IMAGE}\"\n", "", true},
		"unmutated":                {wantDefect: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := copyDevcontainerPinFiles(t)
			if tc.file != "" {
				path := filepath.Join(root, filepath.FromSlash(tc.file))
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read %s: %v", tc.file, err)
				}
				mutated := strings.Replace(string(raw), tc.old, tc.new, 1)
				if mutated == string(raw) {
					t.Fatalf("%s: mutation %q changed nothing; the fixture no longer matches the file", tc.file, tc.old)
				}
				// #nosec G703 -- path is filepath.Join(root, tc.file): root is the t.TempDir() copy copyDevcontainerPinFiles made and tc.file comes from the literal cases table, so no input outside the test chooses the path.
				if err := os.WriteFile(path, []byte(mutated), 0o600); err != nil {
					t.Fatalf("write %s: %v", tc.file, err)
				}
			}
			defects, err := devcontainerPinsDefects(root, envWithoutImagePins())
			if err != nil {
				t.Fatalf("%s: %v", tc.file, err)
			}
			if tc.wantDefect && len(defects) == 0 {
				t.Errorf("%s: mutation %q gave no defect", tc.file, tc.old)
			}
			if !tc.wantDefect && len(defects) != 0 {
				t.Errorf(".devcontainer.json: unmutated copy gave defects %q", defects)
			}
		})
	}
}
