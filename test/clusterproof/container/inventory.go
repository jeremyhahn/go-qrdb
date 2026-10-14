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

// Package container runs the cluster under proof as qrdbd containers: one
// container per node, from the release image built by the repository's
// Dockerfile, which is the image a production deployment runs.
//
// The inventory says everything about the nodes -- their addresses, the image
// tag, where each node's TLS material and data live, and how to reach the
// node's container runtime -- so nothing here names a host. Cluster renders
// the container commands and runs them through a nemesis.Runner, FirstBoot
// takes the fresh cluster through its first boot, and Verify is BOOT-01: the
// admin surface answers on every node over every transport, and the cluster
// runs in raft mode.
package container

import (
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/workload"
)

// The ports a node serves on. They are the image's defaults and every node
// uses the same ones, as in production, where each node is its own host.
const (
	RaftPort = 63001
	RESTPort = 63002
	GRPCPort = 63003
	QUICPort = 63004
)

// HostNetwork is the Inventory.Network that shares each node host's network
// with its container: the deployment for one node per host.
const HostNetwork = "host"

// DefaultDocker runs a docker argv on the machine the harness runs on.
var DefaultDocker = []string{"docker", "$@"}

// Inventory is a container-mode cluster.
type Inventory struct {
	// Image is the qrdbd release image, with its tag.
	Image string `json:"image"`
	// Docker is the command template that runs a docker argv against a
	// node's container runtime, in nemesis.Template form: "$@" stands for
	// the argv and {{.Node}}, {{.Vars.<name>}} and {{shquote .Argv}} are
	// available. Empty is DefaultDocker.
	Docker []string `json:"docker,omitempty"`
	// Network is the docker network every container joins. Empty is
	// HostNetwork. On any other network each node's Address is the IP the
	// container is given on it.
	Network string `json:"network,omitempty"`
	// Credentials are the harness's TLS files: the CA every node's
	// certificate chains to, and the bootstrap admin certificate first boot
	// binds and every admin call presents. The key file is only handed to
	// the SDK transport; nothing here reads it.
	Credentials workload.Credentials `json:"credentials"`
	// FirstBoot holds what first boot provisions.
	FirstBoot FirstBoot `json:"first_boot"`
	// Nodes are the founding members, in replica order.
	Nodes []Node `json:"nodes"`
}

// FirstBoot names the files holding first boot's secrets, so none of them
// is written into the inventory itself.
type FirstBoot struct {
	// SOPINFile and UserPINFile hold the security officer and user PINs.
	SOPINFile   string `json:"so_pin_file"`
	UserPINFile string `json:"user_pin_file"`
	// SecretFile holds the system barrier's secret, for a seal strategy that
	// takes one; empty for one that does not.
	SecretFile string `json:"secret_file,omitempty"`
}

// Node is one founding member and its container.
type Node struct {
	// Name identifies the node in reports and names its container.
	Name string `json:"name"`
	// ReplicaID is the node's Raft replica ID, unique and non-zero.
	ReplicaID uint64 `json:"replica_id"`
	// Address is the host name or IP the other nodes and the harness reach
	// the node at.
	Address string `json:"address"`
	// TLSDir is the directory on the node host holding the node's TLS
	// material as ca.pem, node.pem and node-key.pem. It is mounted into the
	// container read-only.
	TLSDir string `json:"tls_dir"`
	// Data is the node's data directory on the node host, or a docker volume
	// name. First boot needs it empty.
	Data string `json:"data"`
	// Vars are free values for the Docker template.
	Vars map[string]string `json:"vars,omitempty"`
}

// nodeName is a node name: it becomes part of a container name.
var nodeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// LoadInventory reads an inventory file and validates it.
func LoadInventory(path string) (Inventory, error) {
	f, err := os.Open(path)
	if err != nil {
		return Inventory{}, &ConfigError{Field: "path", Reason: err.Error()}
	}
	defer f.Close()
	return ParseInventory(f)
}

// ParseInventory reads an inventory and validates it. An unknown field is an
// error, so a misspelt setting is not silently dropped.
func ParseInventory(r io.Reader) (Inventory, error) {
	var inv Inventory
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&inv); err != nil {
		return Inventory{}, &ConfigError{Field: "json", Reason: err.Error()}
	}
	if inv.Docker == nil {
		inv.Docker = DefaultDocker
	}
	if inv.Network == "" {
		inv.Network = HostNetwork
	}
	return inv, inv.Validate()
}

// Validate checks inv after ParseInventory's defaults.
func (inv Inventory) Validate() error {
	switch {
	case inv.Image == "":
		return &ConfigError{Field: "image", Reason: "the qrdbd image is required"}
	case inv.Credentials.CAFile == "" || inv.Credentials.CertFile == "" || inv.Credentials.KeyFile == "":
		return &ConfigError{Field: "credentials", Reason: "ca_file, cert_file and key_file are all required"}
	case inv.FirstBoot.SOPINFile == "" || inv.FirstBoot.UserPINFile == "":
		return &ConfigError{Field: "first_boot", Reason: "so_pin_file and user_pin_file are required"}
	case len(inv.Nodes) < 3:
		return &ConfigError{Field: "nodes", Reason: "a cluster under proof has at least three founders"}
	}
	names := make(map[string]bool, len(inv.Nodes))
	ids := make(map[uint64]bool, len(inv.Nodes))
	addrs := make(map[string]bool, len(inv.Nodes))
	for i, n := range inv.Nodes {
		field := "nodes[" + strconv.Itoa(i) + "]"
		if err := n.validate(field, inv.Network); err != nil {
			return err
		}
		switch {
		case names[n.Name]:
			return &ConfigError{Field: field, Reason: "duplicate name " + n.Name}
		case ids[n.ReplicaID]:
			return &ConfigError{Field: field, Reason: "duplicate replica_id " + strconv.FormatUint(n.ReplicaID, 10)}
		case addrs[n.Address]:
			return &ConfigError{Field: field, Reason: "duplicate address " + n.Address}
		}
		names[n.Name], ids[n.ReplicaID], addrs[n.Address] = true, true, true
	}
	return nil
}

func (n Node) validate(field, network string) error {
	switch {
	case !nodeName.MatchString(n.Name):
		return &ConfigError{Field: field, Reason: "name must be letters, digits, '.', '_' and '-', at most 64 characters"}
	case n.ReplicaID == 0:
		return &ConfigError{Field: field, Reason: "replica_id must be non-zero"}
	case n.Address == "":
		return &ConfigError{Field: field, Reason: "address is required"}
	case n.TLSDir == "":
		return &ConfigError{Field: field, Reason: "tls_dir is required"}
	case n.Data == "":
		return &ConfigError{Field: field, Reason: "data is required"}
	}
	if network == HostNetwork {
		return nil
	}
	if _, err := netip.ParseAddr(n.Address); err != nil {
		return &ConfigError{Field: field, Reason: "on network " + network + " the address is the container's IP"}
	}
	return nil
}

// Container is the name of n's container.
func (n Node) Container() string { return "clusterproof-" + n.Name }

// RaftAddress is the address the other replicas reach n's Raft transport at.
func (n Node) RaftAddress() string { return net.JoinHostPort(n.Address, strconv.Itoa(RaftPort)) }

// endpoints are the SDK endpoint forms of a node's API, by protocol.
var endpoints = map[string]func(host string) string{
	"quic": func(host string) string { return net.JoinHostPort(host, strconv.Itoa(QUICPort)) },
	"grpc": func(host string) string { return net.JoinHostPort(host, strconv.Itoa(GRPCPort)) },
	"rest": func(host string) string { return "https://" + net.JoinHostPort(host, strconv.Itoa(RESTPort)) },
}

// Endpoint is n's API address for protocol, in the form the SDK transport
// for it takes.
func (n Node) Endpoint(protocol string) (string, error) {
	f, ok := endpoints[protocol]
	if !ok {
		return "", &UnknownProtocolError{Protocol: protocol}
	}
	return f(n.Address), nil
}

// readSecret reads a first-boot secret file. A trailing newline, which an
// editor or echo leaves, is not part of the secret.
func readSecret(field, path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, &ConfigError{Field: field, Reason: err.Error()}
	}
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	if len(b) == 0 {
		clear(b)
		return nil, &ConfigError{Field: field, Reason: "the file is empty"}
	}
	return b, nil
}
