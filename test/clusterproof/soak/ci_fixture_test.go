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
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/container"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/soak"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/workload"
)

// ciScript renders the nightly clusterproof-soak job's configuration,
// relative to this package.
const ciScript = "../../../scripts/cluster-proof-ci.sh"

// ciNemesis is the Makefile's CLUSTERPROOF_CI_NEMESIS default: every fault
// but clock-rate and slow-disk, which need a second kernel and a known block
// device the hosted runner does not have.
var ciNemesis = []string{
	"disk-full", "kill", "partition-bridge",
	"partition-isolate-leader", "partition-majority-minority", "pause",
}

// renderCI runs the script's render subcommand into a temp directory and
// returns the directory and the PROOF_NODES value it printed.
func renderCI(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl is not on PATH")
	}
	dir := t.TempDir()
	out, err := exec.Command("bash", ciScript, "render", dir, "qrdbd:test").Output()
	if err != nil {
		t.Fatalf("%s render: %v", ciScript, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return dir, lines[len(lines)-1]
}

func openFile(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestCIFixtureLoads(t *testing.T) {
	dir, nodes := renderCI(t)

	inv, err := container.ParseInventory(openFile(t, filepath.Join(dir, "inventory.json")))
	if err != nil {
		t.Fatalf("ParseInventory: %v", err)
	}
	if len(inv.Nodes) != 3 || inv.Network != "proof" || inv.Image != "qrdbd:test" {
		t.Fatalf("inventory = %d nodes on %q of %q, want 3 on proof of qrdbd:test", len(inv.Nodes), inv.Network, inv.Image)
	}

	if _, err := workload.ParseConfig(openFile(t, filepath.Join(dir, "workload.json"))); err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	env := map[string]string{
		soak.EnvNodes:    nodes,
		soak.EnvSeal:     "none",
		soak.EnvNemesis:  strings.Join(ciNemesis, ","),
		soak.EnvTargets:  filepath.Join(dir, "targets.json"),
		soak.EnvWorkload: filepath.Join(dir, "workload.json"),
		soak.EnvBundle:   filepath.Join(t.TempDir(), "bundle"),
		soak.EnvDuration: "30m",
	}
	o, err := soak.LoadOptions(func(k string) string { return env[k] }, time.Now())
	if err != nil {
		t.Fatalf("LoadOptions(PROOF_NODES=%s): %v", nodes, err)
	}
	var got []string
	for name := range o.Nemeses {
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, ciNemesis) {
		t.Fatalf("faults built = %v, want %v", got, ciNemesis)
	}
	if len(o.Targets) != 3 {
		t.Fatalf("targets = %d, want 3", len(o.Targets))
	}
}
