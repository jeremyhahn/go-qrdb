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

package soak

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/clockrate"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/nemesis"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/workload"
)

// The environment a run is configured from.
const (
	EnvNodes    = "PROOF_NODES"
	EnvDuration = "PROOF_DURATION"
	EnvNemesis  = "PROOF_NEMESIS"
	EnvBundle   = "PROOF_BUNDLE"
	EnvWorkload = "PROOF_WORKLOAD"
	EnvTargets  = "PROOF_TARGETS"
	EnvSeed     = "PROOF_SEED"
	EnvSeal     = "PROOF_SEAL"
)

// sealTypes are the node seal strategies a run may name in PROOF_SEAL: the
// ones go-qrdb's release candidate ships
// (docs/cluster-verification/architecture.md#seal-types). Hardware-backed
// strategies are go-xkms's and are proven there. The value reports whether a
// node of that type restarts sealed and so needs its own credential before
// it serves again; a none barrier opens itself at boot.
var sealTypes = map[string]bool{
	"none":       false,
	"passphrase": true,
	"shamir":     true,
}

// knownSeal reports whether s is one of sealTypes.
func knownSeal(s string) bool {
	_, ok := sealTypes[s]
	return ok
}

// Defaults of the environment.
const (
	// DefaultDuration is a smoke run. The gating soak length (24 h or 72 h)
	// is pending an owner decision and is always given explicitly.
	DefaultDuration = 5 * time.Minute
	// DefaultBundleRoot holds the bundles a run writes when PROOF_BUNDLE is
	// unset, one directory per run named by its UTC start.
	DefaultBundleRoot = "clusterproof-out"
	// DefaultProtocol is the workload's transport when its file names none.
	DefaultProtocol = "quic"
	// nemesisNone turns the nemesis off.
	nemesisNone = "none"
	// bundleStamp names a bundle directory; it sorts in time order.
	bundleStamp = "2006-01-02T15-04-05Z"
	// maxNodeName bounds a node name, which becomes a bundle path element.
	maxNodeName = 64
)

// Default random schedule bounds.
const (
	DefaultMinGap    = 10 * time.Second
	DefaultMaxGap    = 30 * time.Second
	DefaultMinLength = 10 * time.Second
	DefaultMaxLength = 30 * time.Second
)

// targetsFile is the PROOF_TARGETS file: how to reach each node to inject
// faults, and every fault's parameters. Nothing in it has a default host or
// path; the default fault commands are the nemesis package's.
type targetsFile struct {
	// Exec, Signal and Restart are the nemesis.RemoteConfig templates every
	// node shares; a node's vars fill them in.
	Exec    []string `json:"exec"`
	Signal  []string `json:"signal"`
	Restart []string `json:"restart"`
	// Unseal is the nemesis.RemoteConfig unseal template; a kill under a
	// seal type that restarts sealed requires it.
	Unseal  []string              `json:"unseal"`
	Nodes   map[string]targetNode `json:"nodes"`
	Inspect struct {
		Version []string `json:"version"`
		Config  []string `json:"config"`
	} `json:"inspect"`
	ClockRate struct {
		PPM     int      `json:"ppm"`
		Command []string `json:"command"`
	} `json:"clock_rate"`
	DiskFull struct {
		Path string   `json:"path"`
		Fill []string `json:"fill"`
		Free []string `json:"free"`
	} `json:"disk_full"`
	SlowDisk struct {
		Device         string   `json:"device"`
		Cgroup         string   `json:"cgroup"`
		BytesPerSecond uint64   `json:"bytes_per_second"`
		Throttle       []string `json:"throttle"`
		Unthrottle     []string `json:"unthrottle"`
	} `json:"slow_disk"`
	Partition struct {
		Drop []string `json:"drop"`
		Heal []string `json:"heal"`
	} `json:"partition"`
	Schedule struct {
		MinGap    workload.Duration `json:"min_gap"`
		MaxGap    workload.Duration `json:"max_gap"`
		MinLength workload.Duration `json:"min_length"`
		MaxLength workload.Duration `json:"max_length"`
	} `json:"schedule"`
	HealTimeout     workload.Duration `json:"heal_timeout"`
	RecoveryTimeout workload.Duration `json:"recovery_timeout"`
	Declared        Declared          `json:"declared"`
}

// targetNode is one node's entry in the targets file.
type targetNode struct {
	// Vars fill the node's command templates: its host, container and so on.
	Vars map[string]string `json:"vars"`
	// Addresses are the IPs or prefixes its peers reach it on, for the
	// partitions.
	Addresses []string `json:"addresses"`
}

// faultSpec is one fault the soak can schedule.
type faultSpec struct {
	// selector picks a window's targets; minNodes is the fewest nodes the
	// fault means anything on.
	selector nemesis.Selector
	minNodes int
	build    func(o *Options, tf *targetsFile) (nemesis.Nemesis, error)
}

// faults are the PROOF_NEMESIS names.
var faults = map[string]faultSpec{
	"kill": {nemesis.SelectMinority, 1, func(o *Options, tf *targetsFile) (nemesis.Nemesis, error) {
		if !sealTypes[o.Seal] {
			return nemesis.Kill(), nil
		}
		// A killed node comes back sealed and serves nothing until its own
		// credential unseals it, so the heal must carry that credential.
		if len(tf.Unseal) == 0 {
			return nil, &ConfigError{Field: EnvTargets + " unseal", Reason: "is required for kill under " + EnvSeal + "=" + o.Seal}
		}
		return nemesis.KillSealed(), nil
	}},
	"pause":                       {nemesis.SelectMinority, 1, func(*Options, *targetsFile) (nemesis.Nemesis, error) { return nemesis.Pause(), nil }},
	"partition-majority-minority": {nemesis.SelectAll, 3, partition(func(*Options) nemesis.Grudge { return nemesis.MajorityMinority })},
	"partition-bridge":            {nemesis.SelectAll, 3, partition(func(*Options) nemesis.Grudge { return nemesis.Bridge })},
	"partition-isolate-leader": {nemesis.SelectAll, 2, partition(func(o *Options) nemesis.Grudge {
		// The cluster is asked at inject time, so the partition cuts off
		// whichever node leads then.
		return nemesis.IsolateLeader(func(ctx context.Context) (string, error) { return o.Cluster.Leader(ctx) })
	})},
	// One replica's clock runs at the wrong rate, so the replicas disagree on
	// how long a lease lasts.
	"clock-rate": {nemesis.SelectOne, 1, func(_ *Options, tf *targetsFile) (nemesis.Nemesis, error) {
		if tf.ClockRate.PPM == 0 {
			return nil, &ConfigError{Field: EnvTargets + " clock_rate.ppm", Reason: "is required"}
		}
		// The subcommand refuses the harness's own kernel, which it knows
		// by this boot ID.
		harness, err := clockrate.System{}.BootID()
		if err != nil {
			return nil, &ConfigError{Field: EnvNemesis + " clock-rate", Reason: "cannot read the harness kernel's boot ID", Err: err}
		}
		return nemesis.NewClockRate(nemesis.ClockRateConfig{PPM: tf.ClockRate.PPM, Command: tf.ClockRate.Command, Harness: harness})
	}},
	"disk-full": {nemesis.SelectOne, 1, func(_ *Options, tf *targetsFile) (nemesis.Nemesis, error) {
		if tf.DiskFull.Path == "" {
			return nil, &ConfigError{Field: EnvTargets + " disk_full.path", Reason: "is required"}
		}
		return nemesis.NewDiskFull(nemesis.DiskFullConfig{Path: tf.DiskFull.Path, Fill: tf.DiskFull.Fill, Free: tf.DiskFull.Free})
	}},
	"slow-disk": {nemesis.SelectOne, 1, func(_ *Options, tf *targetsFile) (nemesis.Nemesis, error) {
		d := tf.SlowDisk
		return nemesis.NewSlowDisk(nemesis.SlowDiskConfig{
			Device: d.Device, Cgroup: d.Cgroup, BytesPerSecond: d.BytesPerSecond, Throttle: d.Throttle, Unthrottle: d.Unthrottle,
		})
	}},
}

// partition builds a partition fault on the grudge g picks.
func partition(g func(*Options) nemesis.Grudge) func(*Options, *targetsFile) (nemesis.Nemesis, error) {
	return func(o *Options, tf *targetsFile) (nemesis.Nemesis, error) {
		addrs := make(map[string][]string, len(o.Nodes))
		for _, n := range o.Nodes {
			list := tf.Nodes[n.Name].Addresses
			if len(list) == 0 {
				return nil, &ConfigError{Field: EnvTargets + " nodes." + n.Name + ".addresses", Reason: "is required by the partitions"}
			}
			addrs[n.Name] = list
		}
		return nemesis.NewPartition(nemesis.PartitionConfig{Grudge: g(o), Addresses: addrs, Drop: tf.Partition.Drop, Heal: tf.Partition.Heal})
	}
}

// LoadOptions configures a run from the environment, wiring the SDK
// transports, the remote targets and the faults. now names the default
// bundle.
func LoadOptions(getenv func(string) string, now time.Time) (*Options, error) {
	o := &Options{Duration: DefaultDuration}
	var err error
	if o.Nodes, err = parseNodes(getenv(EnvNodes)); err != nil {
		return nil, err
	}
	if v := getenv(EnvDuration); v != "" {
		if o.Duration, err = time.ParseDuration(v); err != nil || o.Duration <= 0 {
			return nil, &ConfigError{Field: EnvDuration, Reason: "must be a positive Go duration such as 30m or 24h"}
		}
	}
	if o.Seal, err = parseSeal(getenv(EnvSeal)); err != nil {
		return nil, err
	}
	names, err := parseNemesis(getenv(EnvNemesis))
	if err != nil {
		return nil, err
	}
	o.Bundle = getenv(EnvBundle)
	if o.Bundle == "" {
		o.Bundle = filepath.Join(DefaultBundleRoot, now.UTC().Format(bundleStamp))
	}
	seed, err := parseSeed(getenv(EnvSeed))
	if err != nil {
		return nil, err
	}
	tf, err := loadTargets(getenv(EnvTargets), len(names) > 0)
	if err != nil {
		return nil, err
	}
	if o.Workload, err = loadWorkload(getenv(EnvWorkload), o.Nodes, o.Duration, seed); err != nil {
		return nil, err
	}
	if o.Dial, err = workload.NewTransportDialer(o.Workload); err != nil {
		return nil, err
	}
	connect, err := workload.NewTransportConnector(o.Workload)
	if err != nil {
		return nil, err
	}
	cluster := &SDKCluster{Nodes: o.Nodes, Connect: connect}
	o.Cluster, o.Replicas = cluster, cluster
	if o.Targets, err = buildTargets(tf, o.Nodes); err != nil {
		return nil, err
	}
	if o.Targets != nil && (len(tf.Inspect.Version) > 0 || len(tf.Inspect.Config) > 0) {
		o.Inspector = &ExecInspector{Targets: o.Targets, Version: tf.Inspect.Version, Config: tf.Inspect.Config}
	}
	o.HealTimeout = time.Duration(tf.HealTimeout)
	o.RecoveryTimeout = time.Duration(tf.RecoveryTimeout)
	o.Declared = tf.Declared

	o.Schedule = nemesis.Schedule{Seed: seed}
	if len(names) == 0 {
		return o, nil
	}
	if o.Targets == nil {
		return nil, &ConfigError{Field: EnvTargets + " nodes", Reason: "is required when a fault is configured"}
	}
	choices, err := o.buildFaults(tf, names)
	if err != nil {
		return nil, err
	}
	nodes := make([]string, len(o.Nodes))
	for i, n := range o.Nodes {
		nodes[i] = n.Name
	}
	b := tf.Schedule
	o.Schedule, err = nemesis.RandomSchedule(nemesis.RandomConfig{
		Seed:      seed,
		Nodes:     nodes,
		Choices:   choices,
		Duration:  o.Duration,
		MinGap:    cmpOr(time.Duration(b.MinGap), DefaultMinGap),
		MaxGap:    cmpOr(time.Duration(b.MaxGap), DefaultMaxGap),
		MinLength: cmpOr(time.Duration(b.MinLength), DefaultMinLength),
		MaxLength: cmpOr(time.Duration(b.MaxLength), DefaultMaxLength),
	})
	if err != nil {
		return nil, &ConfigError{Field: EnvTargets + " schedule", Reason: "cannot draw a schedule", Err: err}
	}
	if len(o.Schedule.Windows) == 0 {
		return nil, &ConfigError{Field: EnvDuration, Reason: "is too short for one fault window between the schedule's gaps and lengths"}
	}
	return o, nil
}

// buildFaults builds the named faults and the schedule's choices.
func (o *Options) buildFaults(tf *targetsFile, names []string) ([]nemesis.Choice, error) {
	o.Nemeses = make(map[string]nemesis.Nemesis, len(names))
	choices := make([]nemesis.Choice, 0, len(names))
	for _, name := range names {
		spec := faults[name]
		if len(o.Nodes) < spec.minNodes {
			return nil, &ConfigError{Field: EnvNemesis, Reason: name + " needs at least " + strconv.Itoa(spec.minNodes) + " nodes"}
		}
		n, err := spec.build(o, tf)
		if err != nil {
			return nil, &ConfigError{Field: EnvNemesis, Reason: "cannot configure " + name, Err: err}
		}
		if n.Name() != name {
			return nil, &ConfigError{Field: EnvNemesis, Reason: name + " built a fault named " + strconv.Quote(n.Name())}
		}
		o.Nemeses[name] = n
		choices = append(choices, nemesis.Choice{Nemesis: name, Select: spec.selector})
	}
	return choices, nil
}

// parseNodes reads "name=address,...".
func parseNodes(v string) ([]Node, error) {
	if strings.TrimSpace(v) == "" {
		return nil, &ConfigError{Field: EnvNodes, Reason: "is required: comma-separated name=address"}
	}
	var nodes []Node
	seen := make(map[string]bool)
	for item := range strings.SplitSeq(v, ",") {
		name, addr, ok := strings.Cut(strings.TrimSpace(item), "=")
		name, addr = strings.TrimSpace(name), strings.TrimSpace(addr)
		switch {
		case !ok || addr == "":
			return nil, &ConfigError{Field: EnvNodes, Reason: "entry " + strconv.Quote(item) + " is not name=address"}
		case !validNodeName(name):
			return nil, &ConfigError{Field: EnvNodes, Reason: "invalid node name " + strconv.Quote(name) + ": letters, digits, '.', '_' and '-', starting with a letter or digit"}
		case seen[name]:
			return nil, &ConfigError{Field: EnvNodes, Reason: "node " + name + " named twice"}
		}
		seen[name] = true
		nodes = append(nodes, Node{Name: name, Address: addr})
	}
	return nodes, nil
}

// parseNemesis reads the fault names: unset is every fault, "none" is no
// fault. The result is sorted, so the schedule a seed draws does not depend
// on the order the names were given in.
func parseNemesis(v string) ([]string, error) {
	v = strings.TrimSpace(v)
	switch v {
	case "":
		return slices.Sorted(maps.Keys(faults)), nil
	case nemesisNone:
		return nil, nil
	}
	var names []string
	for item := range strings.SplitSeq(v, ",") {
		name := strings.TrimSpace(item)
		switch _, ok := faults[name]; {
		case name == nemesisNone:
			return nil, &ConfigError{Field: EnvNemesis, Reason: "none cannot be combined with a fault"}
		case !ok:
			return nil, &ConfigError{Field: EnvNemesis, Reason: "unknown fault " + strconv.Quote(name) + "; known: " + strings.Join(slices.Sorted(maps.Keys(faults)), ", ")}
		case slices.Contains(names, name):
			return nil, &ConfigError{Field: EnvNemesis, Reason: name + " named twice"}
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

// parseSeal reads PROOF_SEAL. It is required: a run proves the cluster under
// one seal type, and a bundle that does not say which proves nothing about
// any of them.
func parseSeal(v string) (string, error) {
	if !knownSeal(v) {
		return "", &ConfigError{Field: EnvSeal, Reason: "must be one of " + strings.Join(slices.Sorted(maps.Keys(sealTypes)), ", ")}
	}
	return v, nil
}

// parseSeed reads the schedule seed, or draws one. The seed is recorded in
// the bundle whichever way it came.
func parseSeed(v string) (int64, error) {
	if v != "" {
		s, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, &ConfigError{Field: EnvSeed, Reason: "must be a decimal int64", Err: err}
		}
		return s, nil
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, &ConfigError{Field: EnvSeed, Reason: "cannot draw a seed", Err: err}
	}
	return max(int64(binary.LittleEndian.Uint64(b[:])>>1), 1), nil
}

// loadTargets reads the targets file; required is whether a fault needs it.
func loadTargets(path string, required bool) (*targetsFile, error) {
	tf := &targetsFile{}
	if path == "" {
		if required {
			return nil, &ConfigError{Field: EnvTargets, Reason: "is required when a fault is configured"}
		}
		return tf, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &ConfigError{Field: EnvTargets, Reason: "cannot read the file", Err: err}
	}
	if err := decodeStrict(data, tf); err != nil {
		return nil, &ConfigError{Field: EnvTargets, Reason: "malformed", Err: err}
	}
	return tf, nil
}

// loadWorkload reads the workload file through the workload package, or
// builds the default configuration over the nodes.
func loadWorkload(path string, nodes []Node, d time.Duration, seed int64) (workload.Config, error) {
	addrs := make([]string, len(nodes))
	for i, n := range nodes {
		addrs[i] = n.Address
	}
	var cfg workload.Config
	if path == "" {
		cfg = workload.Config{Endpoints: addrs, Seed: uint64(seed)}
		if err := cfg.Normalize(); err != nil {
			return cfg, err
		}
	} else {
		var err error
		if cfg, err = workload.LoadConfig(path); err != nil {
			return cfg, &ConfigError{Field: EnvWorkload, Reason: "invalid workload file", Err: err}
		}
		if !slices.Equal(cfg.Endpoints, addrs) {
			return cfg, &ConfigError{Field: EnvWorkload + " endpoints", Reason: "must be the " + EnvNodes + " addresses, in the same order"}
		}
		if cfg.Duration != 0 && time.Duration(cfg.Duration) != d {
			return cfg, &ConfigError{Field: EnvWorkload + " duration", Reason: "must be unset or equal " + EnvDuration}
		}
	}
	if cfg.Protocol == "" {
		cfg.Protocol = DefaultProtocol
	}
	return cfg, cfg.Validate()
}

// buildTargets builds a remote target per node, or none when the targets
// file names no node.
func buildTargets(tf *targetsFile, nodes []Node) (map[string]nemesis.Target, error) {
	if len(tf.Nodes) == 0 {
		return nil, nil
	}
	want := make([]string, len(nodes))
	for i, n := range nodes {
		want[i] = n.Name
	}
	slices.Sort(want)
	if got := slices.Sorted(maps.Keys(tf.Nodes)); !slices.Equal(got, want) {
		return nil, &ConfigError{Field: EnvTargets + " nodes", Reason: "must name exactly the " + EnvNodes + " nodes"}
	}
	targets := make(map[string]nemesis.Target, len(nodes))
	for _, n := range nodes {
		t, err := nemesis.NewRemoteTarget(nemesis.RemoteConfig{
			Node: n.Name, Vars: tf.Nodes[n.Name].Vars, Exec: tf.Exec, Signal: tf.Signal, Restart: tf.Restart, Unseal: tf.Unseal,
		})
		if err != nil {
			return nil, &ConfigError{Field: EnvTargets, Reason: "cannot build the target of " + n.Name, Err: err}
		}
		targets[n.Name] = t
	}
	return targets, nil
}

// latestBundle is the newest bundle under root: the stamps sort in time
// order.
func latestBundle(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", &ConfigError{Field: EnvBundle, Reason: "is unset and " + root + " cannot be read", Err: err}
	}
	for _, e := range slices.Backward(entries) {
		if e.Type().IsDir() {
			return filepath.Join(root, e.Name()), nil
		}
	}
	return "", &ConfigError{Field: EnvBundle, Reason: "is unset and " + root + " holds no bundle"}
}

// validNodeName reports whether name can name a node in a schedule and a
// directory in a bundle.
func validNodeName(name string) bool {
	if name == "" || len(name) > maxNodeName {
		return false
	}
	for i, r := range []byte(name) {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !alnum && (i == 0 || !bytes.ContainsRune([]byte("._-"), rune(r))) {
			return false
		}
	}
	return true
}

// cmpOr is v, or def when v is zero.
func cmpOr(v, def time.Duration) time.Duration {
	if v == 0 {
		return def
	}
	return v
}
