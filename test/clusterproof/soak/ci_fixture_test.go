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
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/container"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/nemesis"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/soak"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/workload"
)

// ciScript renders the nightly clusterproof-soak job's configuration,
// relative to this package.
const ciScript = "../../../scripts/cluster-proof-ci.sh"

// ciFaultImage is the fault image renderCI passes as render's third argument.
const ciFaultImage = "clusterproof-fault:test"

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
	out, err := exec.Command("bash", ciScript, "render", dir, "qrdbd:test", ciFaultImage).Output()
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

	// The faults run in the fault image, not the release image: the
	// release image has no iptables for the partitions.
	var targets struct {
		Nodes map[string]struct {
			Vars map[string]string `json:"vars"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(openFile(t, filepath.Join(dir, "targets.json"))).Decode(&targets); err != nil {
		t.Fatalf("decode targets.json: %v", err)
	}
	if len(targets.Nodes) != 3 {
		t.Fatalf("targets.json nodes = %d, want 3", len(targets.Nodes))
	}
	for name, n := range targets.Nodes {
		if got := n.Vars["fault_image"]; got != ciFaultImage {
			t.Fatalf("%s fault_image = %q, want %q", name, got, ciFaultImage)
		}
	}
}

// recordRunner records the argv a remote target would run.
type recordRunner struct{ argv []string }

func (r *recordRunner) Run(_ context.Context, argv []string) ([]byte, error) {
	r.argv = argv
	return nil, nil
}

// TestCIFaultArgvWordByWord pins the Codex P1 on #1625: the exec wrapper
// runs without a shell, so the fault's argv must reach docker word by word
// ("$@"), not as one shell-quoted string docker takes for an executable.
func TestCIFaultArgvWordByWord(t *testing.T) {
	dir, _ := renderCI(t)
	var targets struct {
		Exec  []string `json:"exec"`
		Nodes map[string]struct {
			Vars map[string]string `json:"vars"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(openFile(t, filepath.Join(dir, "targets.json"))).Decode(&targets); err != nil {
		t.Fatalf("decode targets.json: %v", err)
	}
	if len(targets.Exec) == 0 || targets.Exec[len(targets.Exec)-1] != nemesis.ArgvElement {
		t.Fatalf("exec = %q, want last element %q", targets.Exec, nemesis.ArgvElement)
	}
	for _, e := range targets.Exec {
		if strings.Contains(e, "shquote") {
			t.Fatalf("exec element %q joins the argv into one word", e)
		}
	}

	node := targets.Nodes["node-1"]
	rec := &recordRunner{}
	target, err := nemesis.NewRemoteTarget(nemesis.RemoteConfig{
		Node:    "node-1",
		Vars:    node.Vars,
		Exec:    targets.Exec,
		Signal:  []string{"true"},
		Restart: []string{"true"},
		Runner:  rec,
	})
	if err != nil {
		t.Fatalf("NewRemoteTarget: %v", err)
	}
	fault := slices.Clone(nemesis.DefaultDiskFill)
	fault[len(fault)-1] = "/data"
	if _, err := target.Exec(context.Background(), fault); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(rec.argv) < len(fault) || !slices.Equal(rec.argv[len(rec.argv)-len(fault):], fault) {
		t.Fatalf("rendered argv = %q, want it to end with the separate words %q", rec.argv, fault)
	}
	if want := node.Vars["data_dir"] + ":/data"; !slices.Contains(rec.argv, want) {
		t.Fatalf("rendered argv = %q, want the node's own filesystem bound as %q", rec.argv, want)
	}
}
