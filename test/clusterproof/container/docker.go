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
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/nemesis"
)

// Where the container sees what the node host mounts into it.
const (
	tlsMount  = "/etc/qrdb/tls"
	dataMount = "/var/lib/qrdb"
	// imageConfig is the configuration file the image ships. Every setting
	// the cluster depends on is a flag, and a flag overrides the file.
	imageConfig = "/etc/qrdb/qrdb.yaml"
)

// Cluster runs an inventory's nodes as containers.
type Cluster struct {
	inv    Inventory
	runner nemesis.Runner
	docker *nemesis.Template
}

// dockerData is what the inventory's Docker template is rendered with.
type dockerData struct {
	Node string
	Vars map[string]string
	Argv []string
}

// NewCluster returns a Cluster that runs inv's container commands through
// runner. inv must have come from ParseInventory or passed Validate.
func NewCluster(inv Inventory, runner nemesis.Runner) (*Cluster, error) {
	if runner == nil {
		return nil, &ConfigError{Field: "runner", Reason: "a command runner is required"}
	}
	docker, err := nemesis.NewTemplate("docker", inv.Docker)
	if err != nil {
		return nil, &ConfigError{Field: "docker", Reason: err.Error()}
	}
	return &Cluster{inv: inv, runner: runner, docker: docker}, nil
}

// Inventory is the cluster's inventory.
func (c *Cluster) Inventory() Inventory { return c.inv }

// Identities returns every node's Raft member identity, read from the node's
// own certificate by the image's node-identity command. The container has
// no network and mounts the TLS directory read-only; node-identity reads the
// certificate alone, never the key.
func (c *Cluster) Identities(ctx context.Context) (map[uint64]string, error) {
	ids := make(map[uint64]string, len(c.inv.Nodes))
	for _, n := range c.inv.Nodes {
		out, err := c.run(ctx, n, "node-identity", []string{
			"run", "--rm", "--network", "none",
			"-v", n.TLSDir + ":" + tlsMount + ":ro",
			c.inv.Image, "node-identity", "--cert", tlsMount + "/node.pem",
		})
		if err != nil {
			return nil, err
		}
		id := lastLine(out)
		if cluster.ValidateMemberIdentity(id) != nil {
			return nil, &IdentityError{Node: n.Name, Output: out}
		}
		ids[n.ReplicaID] = id
	}
	return ids, nil
}

// Start starts every node's container: a static bootstrap of the founders,
// each listing every founder's Raft address and member identity. It stops
// at the first node that fails to start; Remove clears what did.
func (c *Cluster) Start(ctx context.Context, identities map[uint64]string) error {
	for _, n := range c.inv.Nodes {
		if _, ok := identities[n.ReplicaID]; !ok {
			return &ConfigError{Field: "identities", Reason: "no member identity for " + n.Name}
		}
	}
	for _, n := range c.inv.Nodes {
		if _, err := c.run(ctx, n, "start", c.startArgv(n, identities)); err != nil {
			return err
		}
	}
	return nil
}

// startArgv is the docker run command that starts n.
func (c *Cluster) startArgv(n Node, identities map[uint64]string) []string {
	peers := make([]string, len(c.inv.Nodes))
	members := make([]string, len(c.inv.Nodes))
	for i, m := range c.inv.Nodes {
		id := strconv.FormatUint(m.ReplicaID, 10)
		peers[i] = id + "=" + m.RaftAddress()
		members[i] = id + "=" + identities[m.ReplicaID]
	}
	argv := []string{"run", "-d", "--name", n.Container(), "--network", c.inv.Network}
	if c.inv.Network != HostNetwork {
		argv = append(argv, "--ip", n.Address)
	}
	return append(argv,
		"--cap-add", "IPC_LOCK", "--ulimit", "memlock=-1:-1",
		"-v", n.TLSDir+":"+tlsMount+":ro",
		"-v", n.Data+":"+dataMount,
		c.inv.Image,
		"start", "--config", imageConfig,
		"--listen", "0.0.0.0:"+strconv.Itoa(RaftPort),
		"--raft-address", n.RaftAddress(),
		"--data-dir", dataMount,
		"--discovery", "static",
		"--peers", strings.Join(peers, ","),
		"--member-identities", strings.Join(members, ","),
		"--replica-id", strconv.FormatUint(n.ReplicaID, 10),
		"--enable-quic",
		// The gate runs with lease reads on; pinning them here keeps the
		// image's file from turning off what the proof exercises.
		"--lease-read=true", "--check-quorum=true",
		"--tls-ca-file", tlsMount+"/ca.pem",
		"--tls-cert-file", tlsMount+"/node.pem",
		"--tls-key-file", tlsMount+"/node-key.pem",
	)
}

// Logs returns the last tail lines n's container logged.
func (c *Cluster) Logs(ctx context.Context, n Node, tail int) (string, error) {
	return c.run(ctx, n, "logs", []string{"logs", "--tail", strconv.Itoa(tail), n.Container()})
}

// Remove force-removes every node's container, whether or not it is
// running or exists. It tries every node and returns the first failure.
func (c *Cluster) Remove(ctx context.Context) error {
	var first error
	for _, n := range c.inv.Nodes {
		if _, err := c.run(ctx, n, "remove", []string{"rm", "-f", n.Container()}); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// run renders argv through the Docker template for n and runs it.
func (c *Cluster) run(ctx context.Context, n Node, step string, argv []string) (string, error) {
	cmd, err := c.docker.Render(dockerData{Node: n.Name, Vars: n.Vars, Argv: argv}, argv)
	if err != nil {
		return "", &CommandError{Node: n.Name, Step: step, Err: err}
	}
	out, err := c.runner.Run(ctx, cmd)
	text := strings.TrimSpace(string(out))
	if err != nil {
		return "", &CommandError{Node: n.Name, Step: step, Output: text, Err: err}
	}
	return text, nil
}

// lastLine is the last non-empty line of s: a command's answer, after
// anything docker printed first, such as an image pull.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
