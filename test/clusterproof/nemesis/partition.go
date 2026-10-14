//go:build clusterproof

package nemesis

import (
	"context"
	"net/netip"
	"slices"
)

// Grudge decides the shape of a partition: it splits the targeted nodes into
// components, and each node keeps talking to the nodes it shares a component
// with and drops every other. A node may sit in more than one component (a
// bridge).
type Grudge struct {
	// Name names the partition's nemesis: "partition-" + Name.
	Name string
	// Split returns the components of nodes, which arrive in the schedule's
	// (seeded) order.
	Split func(ctx context.Context, nodes []string) ([][]string, error)
}

// MajorityMinority splits the nodes into a minority of the first
// (n-1)/2 and a majority of the rest. It needs at least three nodes.
var MajorityMinority = Grudge{
	Name: "majority-minority",
	Split: func(_ context.Context, nodes []string) ([][]string, error) {
		if len(nodes) < 3 {
			return nil, &ConfigError{Field: "partition-majority-minority targets", Reason: "needs at least three nodes"}
		}
		k := (len(nodes) - 1) / 2
		return [][]string{nodes[:k], nodes[k:]}, nil
	},
}

// Bridge splits the nodes into two halves joined by the middle node, which
// sees both while neither half sees the other. It needs at least three
// nodes.
var Bridge = Grudge{
	Name: "bridge",
	Split: func(_ context.Context, nodes []string) ([][]string, error) {
		if len(nodes) < 3 {
			return nil, &ConfigError{Field: "partition-bridge targets", Reason: "needs at least three nodes"}
		}
		m := len(nodes) / 2
		return [][]string{nodes[:m+1], nodes[m:]}, nil
	},
}

// IsolateLeader cuts the current leader off from every other node. leader
// reports the leader's node name at Inject time; it is the seam to whatever
// the harness asks (a status endpoint, the Raft host).
func IsolateLeader(leader func(ctx context.Context) (string, error)) Grudge {
	return Grudge{
		Name: "isolate-leader",
		Split: func(ctx context.Context, nodes []string) ([][]string, error) {
			if len(nodes) < 2 {
				return nil, &ConfigError{Field: "partition-isolate-leader targets", Reason: "needs at least two nodes"}
			}
			l, err := leader(ctx)
			if err != nil {
				return nil, err
			}
			i := slices.Index(nodes, l)
			if i < 0 {
				return nil, &ConfigError{Field: "partition-isolate-leader targets", Reason: "leader " + l + " is not a target"}
			}
			rest := slices.Delete(slices.Clone(nodes), i, i+1)
			return [][]string{{l}, rest}, nil
		},
	}
}

// DefaultPartitionDrop drops, on the node, all inbound traffic from each
// peer address it is given. It runs in a fault container sharing the node's
// network namespace, keeps its rules in a chain of its own so the heal
// removes them and nothing else, and takes the peers as positional
// arguments, never as shell text.
var DefaultPartitionDrop = []string{"sh", "-c", `set -e
for p in "$@"; do
  case "$p" in *:*) t=ip6tables ;; *) t=iptables ;; esac
  $t -w -N qrdb-nemesis 2>/dev/null || true
  $t -w -C INPUT -j qrdb-nemesis 2>/dev/null || $t -w -I INPUT -j qrdb-nemesis
  $t -w -A qrdb-nemesis -s "$p" -j DROP
done`, "nemesis-partition", ArgvElement}

// DefaultPartitionHeal removes the chain DefaultPartitionDrop installs and
// fails only if it is still there afterwards, so it is idempotent.
var DefaultPartitionHeal = []string{"sh", "-c", `for t in iptables ip6tables; do
  command -v $t >/dev/null 2>&1 || continue
  while $t -w -D INPUT -j qrdb-nemesis 2>/dev/null; do :; done
  $t -w -F qrdb-nemesis 2>/dev/null || true
  $t -w -X qrdb-nemesis 2>/dev/null || true
  if $t -w -n -L qrdb-nemesis >/dev/null 2>&1; then exit 1; fi
done`, "nemesis-partition"}

// PartitionConfig configures a network partition.
type PartitionConfig struct {
	Grudge Grudge
	// Addresses maps each node to the addresses (IPs or CIDR prefixes) its
	// peers reach it on. A node drops traffic from the addresses of every
	// node it shares no component with.
	Addresses map[string][]string
	// Drop is the per-node inject template, rendered with .Node and .Peers
	// and with ArgvElement expanding to the peers; nil is
	// DefaultPartitionDrop.
	Drop []string
	// Heal is the per-node heal template, rendered with .Node; nil is
	// DefaultPartitionHeal.
	Heal []string
}

type partitionData struct {
	Node  string
	Peers []string
}

// NewPartition returns the partition fault cfg describes. It validates every
// address, so nothing but a parsed address ever reaches a command.
func NewPartition(cfg PartitionConfig) (Nemesis, error) {
	if cfg.Grudge.Name == "" || cfg.Grudge.Split == nil {
		return nil, &ConfigError{Field: "partition grudge", Reason: "name and split are required"}
	}
	name := "partition-" + cfg.Grudge.Name
	addrs := make(map[string][]string, len(cfg.Addresses))
	for node, list := range cfg.Addresses {
		if len(list) == 0 {
			return nil, &ConfigError{Field: name + " addresses of " + node, Reason: "must not be empty"}
		}
		for _, a := range list {
			canon, err := canonicalAddress(a)
			if err != nil {
				return nil, &ConfigError{Field: name + " addresses of " + node, Reason: "not an IP address or prefix: " + a, Err: err}
			}
			addrs[node] = append(addrs[node], canon)
		}
	}
	drop, heal, err := templatePair(name, cfg.Drop, DefaultPartitionDrop, cfg.Heal, DefaultPartitionHeal, partitionData{Node: "node", Peers: []string{"192.0.2.1"}})
	if err != nil {
		return nil, err
	}
	plan := func(ctx context.Context, targets []Target) (step, error) {
		peers, err := partitionPeers(ctx, name, cfg.Grudge, addrs, targets)
		if err != nil {
			return nil, err
		}
		return execStep(drop, func(t Target) (any, []string) {
			p := peers[t.Name()]
			return partitionData{Node: t.Name(), Peers: p}, p
		}), nil
	}
	return &fault{
		name: name,
		plan: plan,
		heal: execStep(heal, func(t Target) (any, []string) { return partitionData{Node: t.Name()}, nil }),
	}, nil
}

// partitionPeers returns, per targeted node, the sorted addresses it must
// drop.
func partitionPeers(ctx context.Context, name string, g Grudge, addrs map[string][]string, targets []Target) (map[string][]string, error) {
	nodes := make([]string, len(targets))
	for i, t := range targets {
		nodes[i] = t.Name()
		if _, ok := addrs[nodes[i]]; !ok {
			return nil, &ConfigError{Field: name + " addresses", Reason: "no address for node " + nodes[i]}
		}
	}
	components, err := g.Split(ctx, slices.Clone(nodes))
	if err != nil {
		return nil, err
	}
	// member[n] is the set of component indexes node n belongs to.
	member := make(map[string][]int, len(nodes))
	for i, c := range components {
		for _, n := range c {
			if !slices.Contains(nodes, n) {
				return nil, &ConfigError{Field: name + " split", Reason: "node " + n + " is not a target"}
			}
			member[n] = append(member[n], i)
		}
	}
	peers := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		if len(member[n]) == 0 {
			return nil, &ConfigError{Field: name + " split", Reason: "node " + n + " is in no component"}
		}
		var drop []string
		for _, o := range nodes {
			if o != n && !shareComponent(member[n], member[o]) {
				drop = append(drop, addrs[o]...)
			}
		}
		slices.Sort(drop)
		peers[n] = slices.Compact(drop)
	}
	return peers, nil
}

func shareComponent(a, b []int) bool {
	for _, i := range a {
		if slices.Contains(b, i) {
			return true
		}
	}
	return false
}

// canonicalAddress parses an IP address or CIDR prefix and returns its
// canonical spelling.
func canonicalAddress(s string) (string, error) {
	if a, err := netip.ParseAddr(s); err == nil {
		if a.Zone() != "" {
			return "", &ConfigError{Field: "address " + s, Reason: "zoned addresses cannot be filtered"}
		}
		return a.String(), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return "", err
	}
	return p.Masked().String(), nil
}
