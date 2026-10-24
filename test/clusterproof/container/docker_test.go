//go:build clusterproof

package container

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func newTestCluster(t *testing.T, inv Inventory, r *fakeRunner) *Cluster {
	t.Helper()
	c, err := NewCluster(inv, r)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewClusterRejects(t *testing.T) {
	inv := testInventory(t)
	var ce *ConfigError
	if _, err := NewCluster(inv, nil); !errors.As(err, &ce) || ce.Field != "runner" {
		t.Errorf("nil runner: got %v", err)
	}
	inv.Docker = []string{"$@", "docker"}
	if _, err := NewCluster(inv, &fakeRunner{}); !errors.As(err, &ce) || ce.Field != "docker" {
		t.Errorf("leading $@: got %v", err)
	}
}

func TestIdentitiesThroughWrapper(t *testing.T) {
	inv := testInventory(t)
	inv.Docker = []string{"ssh", "{{.Vars.host}}", "--", "{{shquote .Argv}}"}
	r := &fakeRunner{replies: []reply{{match: "ssh", out: "Unable to find image locally\n" + testIdentity + "\n"}}}
	c := newTestCluster(t, inv, r)
	if c.Inventory().Image != inv.Image {
		t.Error("Inventory lost the image")
	}
	ids, err := c.Identities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[2] != testIdentity {
		t.Errorf("identities %v", ids)
	}
	want := []string{"ssh", "h1", "--", "run --rm --network none -v /srv/n1/tls:/etc/qrdb/tls:ro qrdbd:test node-identity --cert /etc/qrdb/tls/node.pem"}
	if !slices.Equal(r.runs[0], want) {
		t.Errorf("argv\n got %q\nwant %q", r.runs[0], want)
	}
}

func TestIdentitiesRejects(t *testing.T) {
	inv := testInventory(t)
	var ie *IdentityError
	c := newTestCluster(t, inv, &fakeRunner{replies: []reply{{match: "node-identity", out: "spki:XYZ"}}})
	if _, err := c.Identities(context.Background()); !errors.As(err, &ie) || ie.Node != "n1" {
		t.Errorf("bad identity: got %v", err)
	}
	boom := errors.New("exit status 125")
	c = newTestCluster(t, inv, &fakeRunner{replies: []reply{{match: "node-identity", out: "no such image\n", err: boom}}})
	var ce *CommandError
	_, err := c.Identities(context.Background())
	if !errors.As(err, &ce) || ce.Step != "node-identity" || ce.Output != "no such image" || !errors.Is(err, boom) {
		t.Errorf("runner failure: got %v", err)
	}
}

func identities(inv Inventory) map[uint64]string {
	ids := map[uint64]string{}
	for _, n := range inv.Nodes {
		ids[n.ReplicaID] = testIdentity
	}
	return ids
}

func TestStartHostNetwork(t *testing.T) {
	inv := testInventory(t)
	r := &fakeRunner{}
	if err := newTestCluster(t, inv, r).Start(context.Background(), identities(inv)); err != nil {
		t.Fatal(err)
	}
	if len(r.runs) != 3 {
		t.Fatalf("%d runs, want 3", len(r.runs))
	}
	got := strings.Join(r.runs[1], " ")
	for _, want := range []string{
		"docker run -d --name clusterproof-n2 --network host --cap-add IPC_LOCK --ulimit memlock=-1:-1",
		"-v /srv/n2/tls:/etc/qrdb/tls:ro -v /srv/n2/data:/var/lib/qrdb qrdbd:test start --config /etc/qrdb/qrdb.yaml",
		"--raft-address 10.0.0.2:63001 --data-dir /var/lib/qrdb --discovery static",
		"--peers 1=10.0.0.1:63001,2=10.0.0.2:63001,3=10.0.0.3:63001",
		"--member-identities 1=" + testIdentity + ",2=" + testIdentity + ",3=" + testIdentity,
		"--replica-id 2 --enable-quic",
		"--tls-key-file /etc/qrdb/tls/node-key.pem",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("start argv lacks %q:\n%s", want, got)
		}
	}
	if slices.Contains(r.runs[1], "--ip") {
		t.Error("host network got --ip")
	}
}

func TestStartBridgeNetwork(t *testing.T) {
	inv := testInventory(t)
	inv.Network = "proof"
	r := &fakeRunner{}
	if err := newTestCluster(t, inv, r).Start(context.Background(), identities(inv)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.runs[0], " "); !strings.Contains(got, "--network proof --ip 10.0.0.1 ") {
		t.Errorf("bridge argv: %s", got)
	}
}

// The gate runs with lease reads on, so every node's start pins them, and
// the check-quorum they require, as flags: a flag overrides the image's
// configuration file, which could turn either off.
func TestStartPinsLeaseReads(t *testing.T) {
	inv := testInventory(t)
	r := &fakeRunner{}
	if err := newTestCluster(t, inv, r).Start(context.Background(), identities(inv)); err != nil {
		t.Fatal(err)
	}
	if len(r.runs) != len(inv.Nodes) {
		t.Fatalf("%d runs, want %d", len(r.runs), len(inv.Nodes))
	}
	for i, run := range r.runs {
		for _, want := range []string{"--lease-read=true", "--check-quorum=true"} {
			if !slices.Contains(run, want) {
				t.Errorf("node %d start argv lacks %q:\n%s", i+1, want, strings.Join(run, " "))
			}
		}
	}
}

func TestStartRefuses(t *testing.T) {
	inv := testInventory(t)
	r := &fakeRunner{}
	ids := identities(inv)
	delete(ids, 3)
	var ce *ConfigError
	if err := newTestCluster(t, inv, r).Start(context.Background(), ids); !errors.As(err, &ce) || len(r.runs) != 0 {
		t.Errorf("missing identity: got %v after %d runs", err, len(r.runs))
	}
	r = &fakeRunner{replies: []reply{{match: "clusterproof-n2", err: errors.New("name in use")}}}
	var cmd *CommandError
	if err := newTestCluster(t, inv, r).Start(context.Background(), identities(inv)); !errors.As(err, &cmd) || cmd.Node != "n2" || len(r.runs) != 2 {
		t.Errorf("second start fails: got %v after %d runs", err, len(r.runs))
	}
}

func TestLogsAndRemove(t *testing.T) {
	inv := testInventory(t)
	r := &fakeRunner{replies: []reply{{match: "logs", out: "line\n"}, {match: "clusterproof-n1", err: errors.New("daemon down")}}}
	c := newTestCluster(t, inv, r)
	if out, err := c.Logs(context.Background(), inv.Nodes[1], 50); err != nil || out != "line" {
		t.Errorf("logs: %q, %v", out, err)
	}
	if !slices.Equal(r.runs[0], []string{"docker", "logs", "--tail", "50", "clusterproof-n2"}) {
		t.Errorf("logs argv %q", r.runs[0])
	}
	r.runs = nil
	var ce *CommandError
	if err := c.Remove(context.Background()); !errors.As(err, &ce) || ce.Node != "n1" || len(r.runs) != 3 {
		t.Errorf("remove: got %v after %d runs, want n1's failure after all three", err, len(r.runs))
	}
	if !slices.Equal(r.runs[2], []string{"docker", "rm", "-f", "clusterproof-n3"}) {
		t.Errorf("remove argv %q", r.runs[2])
	}
}

func TestRunRenderFailure(t *testing.T) {
	inv := testInventory(t)
	inv.Docker = []string{"ssh", "{{.Vars.missing}}", "$@"}
	r := &fakeRunner{}
	var ce *CommandError
	if _, err := newTestCluster(t, inv, r).Logs(context.Background(), inv.Nodes[0], 1); !errors.As(err, &ce) || len(r.runs) != 0 {
		t.Errorf("render failure: got %v after %d runs", err, len(r.runs))
	}
}
