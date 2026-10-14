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

package container

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/workload"
)

// Protocols are the transports BOOT-01 checks every node over.
var Protocols = []string{"quic", "grpc", "rest"}

// Mode is the deployment mode capabilities reports for a cluster.
const Mode = "raft"

// healthy is the status a serving node's health check reports.
const healthy = "ok"

// Dialer connects to one node's API over one protocol. The caller closes
// the transport.
type Dialer func(ctx context.Context, protocol, endpoint string) (transport.Transport, error)

// NewDialer returns a Dialer over the SDK transports, presenting creds and
// giving each call timeout.
func NewDialer(creds workload.Credentials, timeout time.Duration) (Dialer, error) {
	connectors := make(map[string]workload.Connector, len(Protocols))
	for _, p := range Protocols {
		c, err := workload.NewTransportConnector(workload.Config{Protocol: p, Credentials: creds, OpTimeout: workload.Duration(timeout)})
		if err != nil {
			return nil, err
		}
		connectors[p] = c
	}
	return func(ctx context.Context, protocol, endpoint string) (transport.Transport, error) {
		c, ok := connectors[protocol]
		if !ok {
			return nil, &UnknownProtocolError{Protocol: protocol}
		}
		return c(ctx, endpoint)
	}, nil
}

// bootAPI is what first boot calls on a node.
type bootAPI interface {
	BarrierStatus(ctx context.Context) (*transport.BarrierStatusResponse, error)
	CeremonyInitialize(ctx context.Context, req *transport.CeremonyInitializeRequest) (*transport.CeremonyInitializeResponse, error)
}

// adminAPI is what BOOT-01 asks every node.
type adminAPI interface {
	Health(ctx context.Context) (*transport.HealthResponse, error)
	Status(ctx context.Context) (*transport.StatusResponse, error)
	Peers(ctx context.Context) (*transport.PeersResponse, error)
	Capabilities(ctx context.Context) (*transport.CapabilitiesResponse, error)
	GetTopology(ctx context.Context) (*transport.TopologyResponse, error)
}

// Boot takes an inventory's cluster through first boot and checks it.
type Boot struct {
	Inventory Inventory
	Dial      Dialer
	// Interval is the wait between attempts at a step that has not passed
	// yet. Every step retries until its context ends.
	Interval time.Duration
}

// FirstBoot runs first boot on every node at once: the ceremony that
// initializes the node's barrier with the inventory's PINs and secret and
// binds the harness's certificate as the bootstrap admin. A node already
// through first boot -- its barrier unsealed and initialized -- counts as
// done, so a rerun after a partial first boot finishes it. It runs over
// REST, where the sealed node serves the ceremony.
func (b *Boot) FirstBoot(ctx context.Context) error {
	fb := b.Inventory.FirstBoot
	so, err := readSecret("first_boot.so_pin_file", fb.SOPINFile)
	if err != nil {
		return err
	}
	defer clear(so)
	user, err := readSecret("first_boot.user_pin_file", fb.UserPINFile)
	if err != nil {
		return err
	}
	defer clear(user)
	var secret []byte
	if fb.SecretFile != "" {
		if secret, err = readSecret("first_boot.secret_file", fb.SecretFile); err != nil {
			return err
		}
		defer clear(secret)
	}
	cert, err := os.ReadFile(b.Inventory.Credentials.CertFile)
	if err != nil {
		return &ConfigError{Field: "credentials.cert_file", Reason: err.Error()}
	}
	// The request is built per attempt, so a transport that wipes the
	// secret it sent wipes only its own copy.
	request := func() *transport.CeremonyInitializeRequest {
		return &transport.CeremonyInitializeRequest{
			SOPin: string(so), UserPin: string(user), AdminCertPEM: string(cert), Secret: slices.Clone(secret),
		}
	}
	errs := make([]error, len(b.Inventory.Nodes))
	var wg sync.WaitGroup
	for i, n := range b.Inventory.Nodes {
		wg.Go(func() { errs[i] = b.firstBoot(ctx, n, request) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (b *Boot) firstBoot(ctx context.Context, n Node, request func() *transport.CeremonyInitializeRequest) error {
	return b.retry(ctx, n.Name, "rest", "first boot", func() error {
		api, closeAPI, err := dialAs[bootAPI](ctx, b.Dial, n, "rest", "the first-boot ceremony")
		if err != nil {
			return err
		}
		defer closeAPI()
		if done, err := firstBooted(ctx, api); err != nil || done {
			return err
		}
		req := request()
		_, err = api.CeremonyInitialize(ctx, req)
		clear(req.Secret)
		if err == nil {
			return nil
		}
		// Another attempt may have finished it: the barrier says.
		if done, _ := firstBooted(ctx, api); done {
			return nil
		}
		return err
	})
}

// firstBooted reports whether the node's barrier is initialized and
// unsealed: the state first boot leaves it in.
func firstBooted(ctx context.Context, api bootAPI) (bool, error) {
	st, err := api.BarrierStatus(ctx)
	if err != nil {
		return false, err
	}
	return !st.Sealed && st.InitializedAt != "", nil
}

// Observation is what one node answered over one protocol.
type Observation struct {
	Node        string
	Protocol    string
	Health      string
	IsLeader    bool
	LeaderID    uint64
	RaftAddress string
	Members     []uint64
	ShardCount  int
	Mode        string
}

// String is the observation as one transcript line.
func (o Observation) String() string {
	members := make([]string, len(o.Members))
	for i, m := range o.Members {
		members[i] = strconv.FormatUint(m, 10)
	}
	return o.Node + " over " + o.Protocol + ": health " + o.Health +
		", leader " + strconv.FormatUint(o.LeaderID, 10) + " (self " + strconv.FormatBool(o.IsLeader) + ")" +
		", raft " + o.RaftAddress + ", system shard members " + strings.Join(members, ",") +
		", shards " + strconv.Itoa(o.ShardCount) + ", mode " + o.Mode
}

// Verify is BOOT-01's check: over every protocol, every node answers
// health, status, peers, topology and capabilities; the nodes agree on one
// leader; each reports its own Raft address and every founder in the
// system shard at its Raft address; and capabilities reports raft mode. A
// protocol's round repeats until it passes or ctx ends, so a cluster still
// electing its first leader is waited for, not failed. It returns the
// passing round of every protocol.
func (b *Boot) Verify(ctx context.Context) ([]Observation, error) {
	var transcript []Observation
	for _, p := range Protocols {
		var round []Observation
		err := b.retry(ctx, "every node", p, "BOOT-01", func() error {
			var err error
			round, err = b.observe(ctx, p)
			if err != nil {
				return err
			}
			return b.check(round)
		})
		if err != nil {
			return transcript, err
		}
		transcript = append(transcript, round...)
	}
	return transcript, nil
}

// observe asks every node over protocol.
func (b *Boot) observe(ctx context.Context, protocol string) ([]Observation, error) {
	round := make([]Observation, len(b.Inventory.Nodes))
	for i, n := range b.Inventory.Nodes {
		o, err := b.observeNode(ctx, n, protocol)
		if err != nil {
			return nil, &StepError{Node: n.Name, Protocol: protocol, Step: "admin", Err: err}
		}
		round[i] = o
	}
	return round, nil
}

func (b *Boot) observeNode(ctx context.Context, n Node, protocol string) (Observation, error) {
	api, closeAPI, err := dialAs[adminAPI](ctx, b.Dial, n, protocol, "the admin calls")
	if err != nil {
		return Observation{}, err
	}
	defer closeAPI()
	o := Observation{Node: n.Name, Protocol: protocol}
	health, err := api.Health(ctx)
	if err != nil {
		return o, err
	}
	o.Health, o.IsLeader = health.Status, health.IsLeader
	status, err := api.Status(ctx)
	if err != nil {
		return o, err
	}
	o.LeaderID, o.RaftAddress = status.LeaderID, status.RaftAddress
	peers, err := api.Peers(ctx)
	if err != nil {
		return o, err
	}
	o.Members, err = b.systemMembers(peers)
	if err != nil {
		return o, err
	}
	topology, err := api.GetTopology(ctx)
	if err != nil {
		return o, err
	}
	if topology.RaftAddress != status.RaftAddress {
		return o, &CheckError{Check: "topology", Reason: "raft address " + topology.RaftAddress + " differs from status " + status.RaftAddress}
	}
	o.ShardCount = topology.ShardCount
	caps, err := api.Capabilities(ctx)
	if err != nil {
		return o, err
	}
	o.Mode = caps.Mode
	return o, nil
}

// systemMembers is the system shard's replica IDs, in order, once each
// founder is checked to be listed at its own Raft address.
func (b *Boot) systemMembers(peers *transport.PeersResponse) ([]uint64, error) {
	i := slices.IndexFunc(peers.Shards, func(s transport.ShardPeers) bool { return s.ShardID == shard.ShardKV })
	if i < 0 {
		return nil, &CheckError{Check: "peers", Reason: "no system shard"}
	}
	listed := make(map[uint64]string, len(peers.Shards[i].Peers))
	members := make([]uint64, 0, len(peers.Shards[i].Peers))
	for _, p := range peers.Shards[i].Peers {
		listed[p.ReplicaID] = p.Address
		members = append(members, p.ReplicaID)
	}
	for _, n := range b.Inventory.Nodes {
		if addr, ok := listed[n.ReplicaID]; !ok || addr != n.RaftAddress() {
			return nil, &CheckError{Check: "peers", Reason: n.Name + " is not in the system shard at " + n.RaftAddress()}
		}
	}
	slices.Sort(members)
	return members, nil
}

// check holds one protocol's round to BOOT-01.
func (b *Boot) check(round []Observation) error {
	leader := round[0].LeaderID
	leaders := 0
	for i, o := range round {
		n := b.Inventory.Nodes[i]
		switch {
		case o.Health != healthy:
			return &CheckError{Check: "health", Reason: o.Node + " reports " + strconv.Quote(o.Health)}
		case o.LeaderID == 0:
			return &CheckError{Check: "status", Reason: o.Node + " knows no leader"}
		case o.LeaderID != leader:
			return &CheckError{Check: "status", Reason: "the nodes disagree on the leader"}
		case o.RaftAddress != n.RaftAddress():
			return &CheckError{Check: "status", Reason: o.Node + " reports raft address " + o.RaftAddress + ", want " + n.RaftAddress()}
		case o.ShardCount < 1:
			return &CheckError{Check: "topology", Reason: o.Node + " hosts no shard"}
		case o.Mode != Mode:
			return &CheckError{Check: "capabilities", Reason: o.Node + " reports mode " + strconv.Quote(o.Mode) + ", want " + Mode}
		}
		if o.IsLeader {
			leaders++
			if n.ReplicaID != leader {
				return &CheckError{Check: "health", Reason: o.Node + " says it leads, but the leader is " + strconv.FormatUint(leader, 10)}
			}
		}
	}
	if leaders != 1 {
		return &CheckError{Check: "health", Reason: strconv.Itoa(leaders) + " nodes say they lead"}
	}
	return nil
}

// retry runs attempt until it succeeds or ctx ends, waiting Interval
// between attempts, and reports the last failure if ctx ends first.
func (b *Boot) retry(ctx context.Context, node, protocol, step string, attempt func() error) error {
	for {
		err := attempt()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return &StepError{Node: node, Protocol: protocol, Step: step, Err: errors.Join(err, ctx.Err())}
		case <-time.After(b.Interval):
		}
	}
}

// dialAs connects to n over protocol and narrows the transport to the API a
// step needs. The returned func closes the transport.
func dialAs[API any](ctx context.Context, dial Dialer, n Node, protocol, need string) (API, func(), error) {
	var zero API
	endpoint, err := n.Endpoint(protocol)
	if err != nil {
		return zero, nil, err
	}
	t, err := dial(ctx, protocol, endpoint)
	if err != nil {
		return zero, nil, err
	}
	api, ok := t.(API)
	if !ok {
		_ = t.Close()
		return zero, nil, &UnsupportedTransportError{Protocol: protocol, Need: need}
	}
	return api, func() { _ = t.Close() }, nil
}
