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

//go:build clusterproof

package soak_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
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
	// #1637: an uncapped cluster starved the hosted runner (4 CPUs, 16 GB)
	// until it lost the server. Three nodes stay within 10 GB and 3 CPUs.
	if inv.Memory != "3g" || inv.CPUs != "1" {
		t.Fatalf("inventory caps each node at memory %q cpus %q, want 3g and 1", inv.Memory, inv.CPUs)
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

// TestCIFixtureCertificates proves the rendered certificates carry the
// identities the cluster accepts: the admin certificate passes the
// ceremony's adoption checks and each node certificate names the node by
// its Raft address in the cluster trust domain. adoptAdminCert is
// unexported, so the test runs its parts.
func TestCIFixtureCertificates(t *testing.T) {
	const trustDomain = "qrdb.cluster"
	dir, _ := renderCI(t)

	pool := x509.NewCertPool()
	pool.AddCert(readCert(t, filepath.Join(dir, "tls", "ca.pem")))

	admin := readCert(t, filepath.Join(dir, "admin.pem"))
	if _, err := admin.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("admin certificate does not verify for clientAuth: %v", err)
	}
	if admin.Subject.CommonName != "admin" {
		t.Fatalf("admin CN = %q, want admin", admin.Subject.CommonName)
	}
	if ou := admin.Subject.OrganizationalUnit; len(ou) != 1 || ou[0] != "system" {
		t.Fatalf("admin OU = %v, want [system]", ou)
	}
	if admin.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Fatal("admin certificate lacks digitalSignature")
	}
	tenant, err := auth.TenantFromClientCert(admin, trustDomain)
	if err != nil || tenant != "system" {
		t.Fatalf("TenantFromClientCert(admin) = %q, %v, want system, nil", tenant, err)
	}

	inv, err := container.ParseInventory(openFile(t, filepath.Join(dir, "inventory.json")))
	if err != nil {
		t.Fatalf("ParseInventory: %v", err)
	}
	for _, n := range inv.Nodes {
		want := net.JoinHostPort(n.Address, "63001")
		if got := n.RaftAddress(); got != want {
			t.Fatalf("%s RaftAddress = %q, want %q", n.Name, got, want)
		}
		certFile := filepath.Join(n.TLSDir, "node.pem")
		id, err := spiffe.NodeFromCert(readCert(t, certFile), trustDomain)
		if err != nil || id.NodeID != want {
			t.Fatalf("%s NodeFromCert = %q, %v, want %q", n.Name, id.NodeID, err, want)
		}
		pair, err := tls.LoadX509KeyPair(certFile, filepath.Join(n.TLSDir, "node-key.pem"))
		if err != nil {
			t.Fatalf("%s LoadX509KeyPair: %v", n.Name, err)
		}
		if _, got := forward.NodeIdentity(&tls.Config{Certificates: []tls.Certificate{pair}}); got != want {
			t.Fatalf("%s forward.NodeIdentity = %q, want %q", n.Name, got, want)
		}
	}
}

// readCert parses the PEM certificate in file.
func readCert(t *testing.T, file string) *x509.Certificate {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("%s: no PEM block", file)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return cert
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
