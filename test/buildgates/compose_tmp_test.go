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
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// composeFile is the compose file the CI image's services run under.
const composeFile = "build/docker/devcontainer/compose.yaml"

// composeTmpService is the part of a compose service the /tmp rule reads.
// Volume entries are decoded as nodes because compose accepts both the short
// string form and the long mapping form.
type composeTmpService struct {
	ReadOnly bool        `yaml:"read_only"`
	Tmpfs    []string    `yaml:"tmpfs"`
	Volumes  []yaml.Node `yaml:"volumes"`
}

// composeTmpDefects reports every service in a compose document whose /tmp is
// a tmpfs, or which has a read-only root filesystem and no anonymous volume at
// /tmp. /tmp holds HOME, go's $WORK, the test binaries and every t.TempDir: a
// size-capped tmpfs fills under a parallel cluster selection (run 54973: about
// 400 MB per three-node cluster, a 64 MiB preallocated WAL segment per node)
// and its pages count against the container's memory limit. An anonymous
// volume is on the daemon's disk, as /tmp is on a host, and is removed with
// the container by `run --rm` and `down --volumes`.
func composeTmpDefects(raw []byte) ([]string, error) {
	var doc struct {
		Services map[string]composeTmpService `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var defects []string
	for name, svc := range doc.Services {
		for _, m := range svc.Tmpfs {
			if target, _, _ := strings.Cut(m, ":"); target == "/tmp" {
				defects = append(defects, name+": /tmp is a tmpfs ("+m+")")
			}
		}
		if svc.ReadOnly && !hasAnonymousTmpVolume(svc.Volumes) {
			defects = append(defects, name+": read-only root filesystem and no anonymous volume at /tmp")
		}
	}
	sort.Strings(defects)
	return defects, nil
}

// hasAnonymousTmpVolume reports whether volumes mounts an anonymous volume at
// /tmp: the short form "/tmp", or the long form with type volume, target /tmp
// and no source.
func hasAnonymousTmpVolume(volumes []yaml.Node) bool {
	for _, v := range volumes {
		switch v.Kind {
		case yaml.ScalarNode:
			if v.Value == "/tmp" {
				return true
			}
		case yaml.MappingNode:
			var long struct {
				Type   string `yaml:"type"`
				Source string `yaml:"source"`
				Target string `yaml:"target"`
			}
			if err := v.Decode(&long); err == nil &&
				long.Type == "volume" && long.Source == "" && long.Target == "/tmp" {
				return true
			}
		}
	}
	return false
}

func TestComposeTmpIsOnDisk(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), composeFile))
	if err != nil {
		t.Fatalf("read %s: %v", composeFile, err)
	}
	defects, err := composeTmpDefects(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", composeFile, err)
	}
	for _, d := range defects {
		t.Errorf("%s: %s", composeFile, d)
	}
}

func TestComposeTmpDefectsFixtures(t *testing.T) {
	cases := map[string]struct {
		doc  string
		want int
	}{
		"anonymous short form": {doc: `
services:
  it:
    read_only: true
    volumes: [/tmp, cache:/gocache]
`},
		"anonymous long form": {doc: `
services:
  it:
    read_only: true
    volumes:
      - type: volume
        target: /tmp
`},
		"merged posture": {doc: `
x-posture: &posture
  read_only: true
services:
  it:
    <<: *posture
    volumes: [/tmp]
`},
		"tmpfs": {doc: `
services:
  it:
    read_only: true
    tmpfs: [/tmp:rw,exec,size=3g]
`, want: 2},
		"tmpfs beside the volume": {doc: `
services:
  it:
    read_only: true
    tmpfs: [/tmp:size=3g]
    volumes: [/tmp]
`, want: 1},
		"named volume persists across runs": {doc: `
services:
  it:
    read_only: true
    volumes: [scratch:/tmp]
`, want: 1},
		"merged read-only without /tmp": {doc: `
x-posture: &posture
  read_only: true
services:
  it:
    <<: *posture
`, want: 1},
		"writable root needs no volume": {doc: `
services:
  it:
    image: x
`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			defects, err := composeTmpDefects([]byte(tc.doc))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(defects) != tc.want {
				t.Fatalf("got %d defects %v, want %d", len(defects), defects, tc.want)
			}
		})
	}
}
