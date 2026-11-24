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

// TestStartResourceLimits pins #1637: the inventory's caps reach every
// node's docker run, swap capped with memory, and none without them.
func TestStartResourceLimits(t *testing.T) {
	inv := testInventory(t)
	inv.Memory, inv.CPUs = "3g", "1"
	r := &fakeRunner{}
	if err := newTestCluster(t, inv, r).Start(context.Background(), identities(inv)); err != nil {
		t.Fatal(err)
	}
	for _, run := range r.runs {
		got := strings.Join(run, " ")
		if !strings.Contains(got, "memlock=-1:-1 --memory 3g --memory-swap 3g --cpus 1 -v ") {
			t.Errorf("start argv lacks the caps:\n%s", got)
		}
	}

	inv.Memory, inv.CPUs = "", ""
	r = &fakeRunner{}
	if err := newTestCluster(t, inv, r).Start(context.Background(), identities(inv)); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.runs[0], "--memory") || slices.Contains(r.runs[0], "--cpus") {
		t.Errorf("uncapped inventory still caps: %q", r.runs[0])
	}
}

func TestInventoryResourceLimitsValidate(t *testing.T) {
	for _, tc := range []struct {
		memory, cpus, field string
	}{
		{"3g", "1", ""},
		{"512m", "0.5", ""},
		{"", "", ""},
		{"0g", "", "memory"},
		{"3gb", "", "memory"},
		{"-1", "", "memory"},
		{"", "0", "cpus"},
		{"", "one", "cpus"},
		{"", "-2", "cpus"},
	} {
		inv := testInventory(t)
		inv.Memory, inv.CPUs = tc.memory, tc.cpus
		err := inv.Validate()
		var ce *ConfigError
		switch {
		case tc.field == "" && err != nil:
			t.Errorf("memory %q cpus %q: %v", tc.memory, tc.cpus, err)
		case tc.field != "" && (!errors.As(err, &ce) || ce.Field != tc.field):
			t.Errorf("memory %q cpus %q: got %v, want a %s ConfigError", tc.memory, tc.cpus, err, tc.field)
		}
	}
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

// A fresh host has no containers: docker's "No such container" answer is
// a removed container, not a failure, and any other failure still counts.
func TestRemoveAbsentContainer(t *testing.T) {
	inv := testInventory(t)
	absent := reply{match: "clusterproof-n1", out: "Error response from daemon: No such container: clusterproof-n1", err: errors.New("exit status 1")}
	r := &fakeRunner{replies: []reply{absent}}
	c := newTestCluster(t, inv, r)
	if err := c.Remove(context.Background()); err != nil || len(r.runs) != 3 {
		t.Errorf("remove absent: got %v after %d runs, want nil after all three", err, len(r.runs))
	}
	r.runs = nil
	r.replies = []reply{absent, {match: "clusterproof-n2", err: errors.New("daemon down")}}
	var ce *CommandError
	if err := c.Remove(context.Background()); !errors.As(err, &ce) || ce.Node != "n2" || len(r.runs) != 3 {
		t.Errorf("remove: got %v after %d runs, want n2's failure after all three", err, len(r.runs))
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

func TestStartUsesPerNodeImage(t *testing.T) {
	inv := testInventory(t)
	inv.Nodes[1].Image = "qrdbd:next"
	r := &fakeRunner{}
	if err := newTestCluster(t, inv, r).Start(context.Background(), identities(inv)); err != nil {
		t.Fatal(err)
	}
	want := []string{"qrdbd:test", "qrdbd:next", "qrdbd:test"}
	for i, run := range r.runs {
		// The image is the argument docker run's command follows.
		if at := slices.Index(run, "start"); at < 1 || run[at-1] != want[i] {
			t.Errorf("node %d argv %q, want image %s", i+1, run, want[i])
		}
	}
	r.runs = nil
	r.replies = []reply{{match: "node-identity", out: testIdentity + "\n"}}
	if _, err := newTestCluster(t, inv, r).Identities(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.runs[1], "qrdbd:next") {
		t.Errorf("node-identity argv %q, want qrdbd:next", r.runs[1])
	}
}

func TestReplaceKeepsDataVolume(t *testing.T) {
	inv := testInventory(t)
	r := &fakeRunner{}
	c := newTestCluster(t, inv, r)
	ids := identities(inv)
	if err := c.Start(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	before := slices.Clone(r.runs[1])
	r.runs = nil
	if err := c.Replace(context.Background(), inv.Nodes[1], "qrdbd:next", ids); err != nil {
		t.Fatal(err)
	}
	if len(r.runs) != 2 || !slices.Equal(r.runs[0], []string{"docker", "rm", "-f", "clusterproof-n2"}) {
		t.Fatalf("replace runs %q, want rm then run", r.runs)
	}
	after := r.runs[1]
	if !slices.Contains(after, "/srv/n2/data:"+dataMount) {
		t.Errorf("replace argv %q lost the data volume", after)
	}
	// The same start, image aside.
	swapped := slices.Clone(before)
	swapped[slices.Index(swapped, "qrdbd:test")] = "qrdbd:next"
	if !slices.Equal(after, swapped) {
		t.Errorf("replace argv\n %q\nwant\n %q", after, swapped)
	}
	if got := c.Inventory().ImageOf(c.Inventory().Nodes[1]); got != "qrdbd:next" {
		t.Errorf("inventory image after replace = %q", got)
	}
	if inv.Nodes[1].Image != "" {
		t.Errorf("replace mutated the caller's inventory: %q", inv.Nodes[1].Image)
	}

	// Rolling back is the same step; an absent container is not an error.
	r.runs = nil
	r.replies = []reply{{match: "rm", out: "Error response from daemon: No such container: clusterproof-n2", err: errors.New("exit status 1")}}
	if err := c.Replace(context.Background(), inv.Nodes[1], "qrdbd:test", ids); err != nil {
		t.Fatalf("replace absent: %v", err)
	}
	if !slices.Equal(r.runs[1], before) {
		t.Errorf("roll back argv %q, want %q", r.runs[1], before)
	}
}

func TestReplaceRefuses(t *testing.T) {
	inv := testInventory(t)
	ids := identities(inv)
	stranger := Node{Name: "x", ReplicaID: 9}
	var ce *ConfigError
	for name, call := range map[string]func(*Cluster) error{
		"stranger":    func(c *Cluster) error { return c.Replace(context.Background(), stranger, "qrdbd:next", ids) },
		"empty image": func(c *Cluster) error { return c.Replace(context.Background(), inv.Nodes[0], "", ids) },
		"no identity": func(c *Cluster) error {
			return c.Replace(context.Background(), inv.Nodes[0], "qrdbd:next", map[uint64]string{})
		},
	} {
		r := &fakeRunner{}
		if err := call(newTestCluster(t, inv, r)); !errors.As(err, &ce) || len(r.runs) != 0 {
			t.Errorf("%s: got %v after %d runs, want ConfigError before any run", name, err, len(r.runs))
		}
	}
	r := &fakeRunner{replies: []reply{{match: "run", err: errors.New("daemon down")}}}
	var cmd *CommandError
	if err := newTestCluster(t, inv, r).Replace(context.Background(), inv.Nodes[0], "qrdbd:next", ids); !errors.As(err, &cmd) {
		t.Errorf("start failure: got %v, want CommandError", err)
	}
}
