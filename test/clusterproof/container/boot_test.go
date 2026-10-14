//go:build clusterproof

package container

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/workload"
)

func shortCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func TestFirstBootEveryNode(t *testing.T) {
	fc := newFakeCluster(t)
	if err := fc.boot().FirstBoot(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, n := range fc.nodes {
		if !n.booted || len(n.ceremonies) != 1 {
			t.Fatalf("node %d: booted %v after %d ceremonies", n.replicaID, n.booted, len(n.ceremonies))
		}
		req := n.ceremonies[0]
		if req.SOPin != "so-pin" || req.UserPin != "user-pin" || req.AdminCertPEM != "admin-cert" || string(req.Secret) != "barrier-secret" {
			t.Errorf("node %d request %+v", n.replicaID, req)
		}
		if n.closed == 0 {
			t.Errorf("node %d transport never closed", n.replicaID)
		}
	}
	for _, d := range fc.dials {
		if !strings.HasPrefix(d, "rest https://") {
			t.Errorf("first boot dialed %q, want rest", d)
		}
	}
}

func TestFirstBootWithoutSecret(t *testing.T) {
	fc := newFakeCluster(t)
	fc.inv.FirstBoot.SecretFile = ""
	if err := fc.boot().FirstBoot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := fc.node(0).ceremonies[0].Secret; s != nil {
		t.Errorf("secret %q sent with no secret file", s)
	}
}

func TestFirstBootSkipsBootedNode(t *testing.T) {
	fc := newFakeCluster(t)
	fc.node(1).booted = true
	if err := fc.boot().FirstBoot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fc.node(1).ceremonies) != 0 {
		t.Error("a node through first boot was initialized again")
	}
}

func TestFirstBootErrorThatBooted(t *testing.T) {
	fc := newFakeCluster(t)
	fc.set(func(n *fakeNode) { n.bootErr, n.bootAnyway = errors.New("already initialized"), true })
	if err := fc.boot().FirstBoot(context.Background()); err != nil {
		t.Fatalf("a ceremony that failed after the barrier came up: %v", err)
	}
}

func TestFirstBootRetriesUntilDeadline(t *testing.T) {
	fc := newFakeCluster(t)
	boom := errors.New("sealed window not open")
	fc.node(2).bootErr = boom
	err := fc.boot().FirstBoot(shortCtx(t))
	var se *StepError
	if !errors.As(err, &se) || se.Node != "n3" || se.Step != "first boot" || !errors.Is(err, boom) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if len(fc.node(2).ceremonies) < 2 {
		t.Error("the failing node was not retried")
	}
	if !fc.node(0).booted {
		t.Error("one node's failure stopped another's first boot")
	}
}

func TestFirstBootBarrierUnreachable(t *testing.T) {
	fc := newFakeCluster(t)
	fc.set(func(n *fakeNode) { n.statusErr = errors.New("connection refused") })
	var se *StepError
	if err := fc.boot().FirstBoot(shortCtx(t)); !errors.As(err, &se) {
		t.Fatalf("got %v", err)
	}
	if len(fc.node(0).ceremonies) != 0 {
		t.Error("ceremony ran without a barrier status")
	}
}

func TestFirstBootConfigErrors(t *testing.T) {
	for name, mutate := range map[string]func(*Inventory){
		"so pin":   func(inv *Inventory) { inv.FirstBoot.SOPINFile += ".missing" },
		"user pin": func(inv *Inventory) { inv.FirstBoot.UserPINFile += ".missing" },
		"secret":   func(inv *Inventory) { inv.FirstBoot.SecretFile += ".missing" },
		"cert":     func(inv *Inventory) { inv.Credentials.CertFile += ".missing" },
	} {
		t.Run(name, func(t *testing.T) {
			fc := newFakeCluster(t)
			mutate(&fc.inv)
			var ce *ConfigError
			if err := fc.boot().FirstBoot(context.Background()); !errors.As(err, &ce) || len(fc.dials) != 0 {
				t.Errorf("got %v after %d dials", err, len(fc.dials))
			}
		})
	}
}

func TestVerifyHealthyCluster(t *testing.T) {
	fc := newFakeCluster(t)
	transcript, err := fc.boot().Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != len(Protocols)*3 {
		t.Fatalf("%d observations", len(transcript))
	}
	seen := map[string]bool{}
	for _, o := range transcript {
		seen[o.Protocol] = true
		if o.Mode != Mode || o.LeaderID != 1 || len(o.Members) != 3 {
			t.Errorf("observation %+v", o)
		}
	}
	for _, p := range Protocols {
		if !seen[p] {
			t.Errorf("no observation over %s", p)
		}
	}
	want := "n1 over quic: health ok, leader 1 (self true), raft 10.0.0.1:63001, system shard members 1,2,3, shards 1, mode raft"
	if got := transcript[0].String(); got != want {
		t.Errorf("transcript line\n got %q\nwant %q", got, want)
	}
}

func TestVerifyWaitsForLeader(t *testing.T) {
	fc := newFakeCluster(t)
	fc.set(func(n *fakeNode) { n.leader = 0 })
	go func() {
		time.Sleep(10 * time.Millisecond)
		fc.set(func(n *fakeNode) { n.leader = 2 })
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := fc.boot().Verify(ctx); err != nil {
		t.Fatalf("a cluster that elects a leader: %v", err)
	}
}

func TestVerifyFails(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(fc *fakeCluster)
		check  string
	}{
		"unhealthy":    {func(fc *fakeCluster) { fc.node(1).health = "sealed" }, "health"},
		"no leader":    {func(fc *fakeCluster) { fc.set(func(n *fakeNode) { n.leader = 0 }) }, "status"},
		"split":        {func(fc *fakeCluster) { fc.node(2).leader = 2 }, "status"},
		"raft address": {func(fc *fakeCluster) { fc.node(1).raft, fc.node(1).topoRaft = "10.0.0.9:63001", "10.0.0.9:63001" }, "status"},
		"topology":     {func(fc *fakeCluster) { fc.node(1).topoRaft = "10.0.0.9:63001" }, "topology"},
		"standalone":   {func(fc *fakeCluster) { fc.node(0).mode = "standalone" }, "capabilities"},
		"no shard":     {func(fc *fakeCluster) { fc.node(0).shards = nil }, "peers"},
		"other shard":  {func(fc *fakeCluster) { fc.node(0).shards[0].ShardID = shard.ShardKV + 100 }, "peers"},
		"missing peer": {func(fc *fakeCluster) { fc.node(0).shards[0].Peers = fc.node(0).shards[0].Peers[:2] }, "peers"},
		"moved peer":   {func(fc *fakeCluster) { fc.node(0).shards[0].Peers[1].Address = "10.0.0.9:63001" }, "peers"},
		"no one leads": {func(fc *fakeCluster) { fc.set(func(n *fakeNode) { n.leader = 9 }) }, "health"},
	} {
		t.Run(name, func(t *testing.T) {
			fc := newFakeCluster(t)
			tc.mutate(fc)
			_, err := fc.boot().Verify(shortCtx(t))
			var se *StepError
			var ce *CheckError
			if !errors.As(err, &se) || se.Step != "BOOT-01" || !errors.As(err, &ce) || ce.Check != tc.check {
				t.Fatalf("got %v, want a %s CheckError", err, tc.check)
			}
		})
	}
}

func TestCheckLeaderClaims(t *testing.T) {
	fc := newFakeCluster(t)
	b := fc.boot()
	round, err := b.observe(context.Background(), "grpc")
	if err != nil {
		t.Fatal(err)
	}
	two := append([]Observation(nil), round...)
	two[1].IsLeader = true
	var ce *CheckError
	if err := b.check(two); !errors.As(err, &ce) || ce.Check != "health" {
		t.Errorf("a follower claiming to lead: got %v", err)
	}
	none := append([]Observation(nil), round...)
	none[0].IsLeader = false
	if err := b.check(none); !errors.As(err, &ce) || !strings.Contains(ce.Reason, "0 nodes") {
		t.Errorf("no node claiming to lead: got %v", err)
	}
}

func TestVerifyAdminError(t *testing.T) {
	fc := newFakeCluster(t)
	boom := errors.New("unavailable")
	fc.node(2).adminErr = boom
	transcript, err := fc.boot().Verify(shortCtx(t))
	if !errors.Is(err, boom) || transcript != nil {
		t.Fatalf("got %v with %d observations", err, len(transcript))
	}
}

func TestDialAs(t *testing.T) {
	n := Node{Name: "n1", Address: "10.0.0.1"}
	closed := 0
	bare := func(context.Context, string, string) (transport.Transport, error) { return bareTransport{&closed}, nil }
	var ue *UnsupportedTransportError
	if _, _, err := dialAs[adminAPI](context.Background(), bare, n, "grpc", "the admin calls"); !errors.As(err, &ue) || closed != 1 {
		t.Errorf("unsupported: got %v, closed %d", err, closed)
	}
	boom := errors.New("refused")
	failing := func(context.Context, string, string) (transport.Transport, error) { return nil, boom }
	if _, _, err := dialAs[adminAPI](context.Background(), failing, n, "grpc", "x"); !errors.Is(err, boom) {
		t.Errorf("dial failure: got %v", err)
	}
	var upe *UnknownProtocolError
	if _, _, err := dialAs[adminAPI](context.Background(), failing, n, "ftp", "x"); !errors.As(err, &upe) {
		t.Errorf("unknown protocol: got %v", err)
	}
}

func TestNewDialer(t *testing.T) {
	dial, err := NewDialer(workload.Credentials{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var upe *UnknownProtocolError
	if _, err := dial(context.Background(), "ftp", "10.0.0.1:1"); !errors.As(err, &upe) {
		t.Errorf("unknown protocol: got %v", err)
	}
}
