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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// devcontainerFile is the VS Code Dev Containers definition at the
// repository root.
const devcontainerFile = ".devcontainer.json"

// devcontainerRunServicesDefects reports why Reopen in Container would start
// more than the devcontainer service, or a service it cannot build. Without
// runServices, Dev Containers runs compose up on every service in the file,
// including the CI-only services whose images are never built locally.
func devcontainerRunServicesDefects(devcontainer, compose []byte) ([]string, error) {
	var lines []string
	for _, l := range strings.Split(string(devcontainer), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "//") {
			continue
		}
		lines = append(lines, l)
	}
	var dc struct {
		Service           string   `json:"service"`
		RunServices       []string `json:"runServices"`
		DockerComposeFile []string `json:"dockerComposeFile"`
	}
	if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &dc); err != nil {
		return nil, fmt.Errorf("devcontainer: %w", err)
	}
	var doc struct {
		Services map[string]struct {
			PullPolicy string     `yaml:"pull_policy"`
			Build      *yaml.Node `yaml:"build"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, fmt.Errorf("compose: %w", err)
	}
	var defects []string
	if len(dc.RunServices) != 1 || dc.RunServices[0] != dc.Service {
		defects = append(defects, fmt.Sprintf("runServices is %q, want exactly [%q]: without it Reopen in Container starts every compose service", dc.RunServices, dc.Service))
	}
	for _, name := range dc.RunServices {
		svc, ok := doc.Services[name]
		switch {
		case !ok:
			defects = append(defects, fmt.Sprintf("runServices names %q, which is not a compose service", name))
		case svc.PullPolicy == "never" && svc.Build == nil:
			defects = append(defects, fmt.Sprintf("runServices names %q, which has pull_policy: never and no build: section", name))
		}
	}
	return defects, nil
}

func TestDevcontainerRunsOnlyDevcontainerService(t *testing.T) {
	root := repoRoot(t)
	dc, err := os.ReadFile(filepath.Join(root, devcontainerFile))
	if err != nil {
		t.Fatalf("read %s: %v", devcontainerFile, err)
	}
	compose, err := os.ReadFile(filepath.Join(root, composeFile))
	if err != nil {
		t.Fatalf("read %s: %v", composeFile, err)
	}
	defects, err := devcontainerRunServicesDefects(dc, compose)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, d := range defects {
		t.Errorf("%s: %s", devcontainerFile, d)
	}
}

func TestDevcontainerRunServicesDefectsFixtures(t *testing.T) {
	const compose = `
x-dev: &dev
  build:
    context: .
services:
  devcontainer:
    <<: *dev
    image: qrdb-dev:local
    pull_policy: never
  integration:
    image: qrdb-it:local
    pull_policy: never
`
	const header = "// Copyright 2026\n//\n// Licensed under the Apache License.\n\n"
	cases := map[string]struct {
		dc   string
		want int
	}{
		"no runServices": {dc: header + `{
	"dockerComposeFile": ["compose.yaml"],
	"service": "devcontainer"
}`, want: 1},
		"CI service in runServices": {dc: header + `{
	"dockerComposeFile": ["compose.yaml"],
	"service": "devcontainer",
	"runServices": ["devcontainer", "integration"]
}`, want: 2},
		"unknown service": {dc: header + `{
	"service": "missing",
	"runServices": ["missing"]
}`, want: 1},
		"only the devcontainer service": {dc: header + `{
	"dockerComposeFile": ["compose.yaml"],
	"service": "devcontainer",
	"runServices": ["devcontainer"]
}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			defects, err := devcontainerRunServicesDefects([]byte(tc.dc), []byte(compose))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(defects) != tc.want {
				t.Fatalf("got %d defects %v, want %d", len(defects), defects, tc.want)
			}
		})
	}
}
