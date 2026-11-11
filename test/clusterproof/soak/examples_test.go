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

package soak_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/container"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/soak"
)

// examplesDir holds the operator's example configuration for the gate run,
// docs/cluster-verification/examples, relative to this package.
const examplesDir = "../../../docs/cluster-verification/examples"

// exampleNodes is the PROOF_NODES value the examples are written for: the
// workload's endpoints, in order, under the targets file's node names.
const exampleNodes = "node-1=db1.example.com:63004,node-2=db2.example.com:63004,node-3=db3.example.com:63004"

// exampleFaults is every fault engineering.md "The nemesis" lists; the
// targets example must configure each one.
var exampleFaults = []string{
	"clock-rate", "disk-full", "kill", "partition-bridge",
	"partition-isolate-leader", "partition-majority-minority", "pause", "slow-disk",
}

func openExample(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join(examplesDir, name))
	if err != nil {
		t.Fatalf("open example %s: %v", name, err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// exampleEnv is the gate run's environment over the examples: passphrase
// seal and no PROOF_NEMESIS, so every fault is built from targets.
func exampleEnv(t *testing.T, targets string) func(string) string {
	t.Helper()
	env := map[string]string{
		soak.EnvNodes:    exampleNodes,
		soak.EnvSeal:     "passphrase",
		soak.EnvTargets:  targets,
		soak.EnvWorkload: filepath.Join(examplesDir, "workload.json"),
		soak.EnvBundle:   t.TempDir(),
		soak.EnvSeed:     "679",
		soak.EnvDuration: "1h",
	}
	return func(k string) string { return env[k] }
}

func TestExampleInventoryParses(t *testing.T) {
	inv, err := container.ParseInventory(openExample(t, "inventory.json"))
	if err != nil {
		t.Fatalf("ParseInventory(inventory.json): %v", err)
	}
	if len(inv.Nodes) != 3 {
		t.Fatalf("inventory.json nodes = %d, want 3", len(inv.Nodes))
	}
}

func TestExampleTargetsBuildEveryFault(t *testing.T) {
	o, err := soak.LoadOptions(exampleEnv(t, filepath.Join(examplesDir, "targets.json")), time.Now())
	if err != nil {
		t.Fatalf("LoadOptions(targets.json): %v", err)
	}
	var got []string
	for name := range o.Nemeses {
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, exampleFaults) {
		t.Fatalf("faults built = %v, want %v", got, exampleFaults)
	}
	if len(o.Targets) != 3 {
		t.Fatalf("targets = %d, want 3", len(o.Targets))
	}
	if o.Declared.ReadMode == "" {
		t.Fatal("targets.json declares no read mode")
	}
	if o.Workload.Protocol != "quic" {
		t.Fatalf("workload protocol = %q, want quic", o.Workload.Protocol)
	}
}

// TestExampleTargetsMissingTemplateRefused removes one fault's template from
// the targets example at a time and requires LoadOptions to refuse it, so
// the test above cannot pass on a loader that accepts anything.
func TestExampleTargetsMissingTemplateRefused(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(examplesDir, "targets.json"))
	if err != nil {
		t.Fatalf("read targets.json: %v", err)
	}
	for _, tc := range []struct {
		fault  string
		want   string
		remove func(map[string]any)
	}{
		{"kill", "unseal", func(m map[string]any) { delete(m, "unseal") }},
		{"clock-rate", "clock_rate.ppm", func(m map[string]any) { delete(m, "clock_rate") }},
		{"disk-full", "disk_full.path", func(m map[string]any) { delete(m, "disk_full") }},
		{"slow-disk", "slow-disk", func(m map[string]any) { delete(m, "slow_disk") }},
		{"partitions", "addresses", func(m map[string]any) {
			delete(m["nodes"].(map[string]any)["node-2"].(map[string]any), "addresses")
		}},
	} {
		t.Run(tc.fault, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("decode targets.json: %v", err)
			}
			tc.remove(m)
			out, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "targets.json")
			if err := os.WriteFile(path, out, 0o600); err != nil {
				t.Fatal(err)
			}
			o, err := soak.LoadOptions(exampleEnv(t, path), time.Now())
			if err == nil {
				t.Fatalf("LoadOptions accepted targets without the %s template (faults %d)", tc.fault, len(o.Nemeses))
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadOptions without the %s template: %v, want an error naming %q", tc.fault, err, tc.want)
			}
		})
	}
}
