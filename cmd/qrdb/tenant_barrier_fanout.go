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

package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A tenant barrier served by several nodes holds its own key on each, and no
// key is ever sent between them, so a request only the operator can make --
// an init carrying the passphrase -- reaches every node from the operator's
// client. The nodes serving the barrier are read from the status --server
// reports, and the one request is sent to each over the transport --server is
// reached on, at the address each node's member record publishes for it.

// servingAddresses turns the endpoint a node publishes for a client API
// transport into the address that transport dials, as --server would name it.
// unix and embedded reach only the node they run beside, so they are absent.
var servingAddresses = map[string]func(endpoint string) string{
	transportREST: func(ep string) string { return "https://" + ep },
	transportGRPC: func(ep string) string { return ep },
	transportQUIC: func(ep string) string { return ep },
	transportMCP:  func(ep string) string { return "https://" + ep + "/mcp" },
}

// servingNode is one node serving a tenant's barrier: its Raft address and a
// client connected to it, or why none could be.
type servingNode struct {
	addr   string
	client *sdk.Client
	err    error
}

// nodeResult is one node's answer to a request sent to every node serving a
// barrier.
type nodeResult struct {
	Node  string `json:"node" yaml:"node"`
	Error string `json:"error,omitempty" yaml:"error,omitempty"`
}

// BarrierFanOutError reports the nodes that refused a request sent to every
// node serving a barrier, or could not be reached. The others accepted it.
type BarrierFanOutError struct {
	Op     string
	Failed []string
}

// Error returns a human-readable description.
func (e *BarrierFanOutError) Error() string {
	return "qrdb: " + e.Op + " was refused by " + strings.Join(e.Failed, ", ")
}

// NodeEndpointMissingError reports a node serving a barrier whose member
// record publishes no endpoint for the transport the request is sent over.
type NodeEndpointMissingError struct {
	Node      string
	Transport string
}

// Error returns a human-readable description.
func (e *NodeEndpointMissingError) Error() string {
	return "qrdb: node " + e.Node + " publishes no " + e.Transport + " endpoint"
}

// nodeDialer connects to the nodes serving a barrier over the root command's
// TLS flags, the ones --server is reached with.
type nodeDialer struct {
	caFile   string
	certFile string
	keyFile  string
	timeout  time.Duration
	clients  []*sdk.Client
}

// newNodeDialer reads the root command's connection flags.
func newNodeDialer(cmd *cobra.Command) *nodeDialer {
	root := cmd.Root().PersistentFlags()
	d := &nodeDialer{}
	d.caFile, _ = root.GetString("ca-file")
	d.certFile, _ = root.GetString("cert-file")
	d.keyFile, _ = root.GetString("key-file")
	d.timeout, _ = root.GetDuration("timeout")
	return d
}

// dial connects to address over transportType.
func (d *nodeDialer) dial(ctx context.Context, transportType, address string) (*sdk.Client, error) {
	cl, err := buildClient(ctx, transportType, address, d.caFile, d.certFile, d.keyFile, d.timeout)
	if err != nil {
		return nil, err
	}
	d.clients = append(d.clients, cl)
	return cl, nil
}

// close disconnects every client dial made.
func (d *nodeDialer) close() {
	for _, cl := range d.clients {
		_ = cl.Close()
	}
}

// serverTransport is the transport --server is reached on, resolved as the
// root command resolves it: a URL scheme on --server overrides a --transport
// left at its default.
func serverTransport(cmd *cobra.Command) string {
	root := cmd.Root().PersistentFlags()
	serverFlag, _ := root.GetString("server")
	transportFlag, _ := root.GetString("transport")
	if !root.Changed("transport") && serverHasScheme(serverFlag) {
		transportFlag = ""
	}
	transportType, _ := resolveTransport(serverFlag, transportFlag)
	return transportType
}

// dialServingNodes connects to every node in nodes over --server's transport.
// A node that cannot be reached is returned with why, so the others still
// answer. The returned closer disconnects every client dialled.
func dialServingNodes(cmd *cobra.Command, nodes []transport.TenantBarrierNode) ([]servingNode, func(), error) {
	transportType := serverTransport(cmd)
	address, ok := servingAddresses[transportType]
	if !ok {
		return nil, nil, &InvalidArgError{
			Arg:    "--transport",
			Reason: "a barrier served by several nodes is reached on each over rest, grpc, quic or mcp, not " + transportType,
		}
	}
	d := newNodeDialer(cmd)
	out := make([]servingNode, 0, len(nodes))
	for _, n := range nodes {
		node := servingNode{addr: n.Node}
		if ep := n.Endpoints[transportType]; ep == "" {
			node.err = &NodeEndpointMissingError{Node: n.Node, Transport: transportType}
		} else {
			node.client, node.err = d.dial(cmd.Context(), transportType, address(ep))
		}
		out = append(out, node)
	}
	return out, d.close, nil
}

// sendToServingNodes sends op to every node at once and reports them in the
// order given. A node's init waits, up to the group's startup budget, for its
// replica of the tenant's home group to have a leader, which needs a quorum of
// the group's nodes initialized: sent one node at a time, the first would hold
// the fan-out for that whole budget. A node that refuses op, or could not be reached, does
// not stop the others; they are reported together as *BarrierFanOutError
// naming opName.
func sendToServingNodes(ctx context.Context, nodes []servingNode, opName string, op func(context.Context, transport.TenantClient) error) ([]nodeResult, error) {
	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		if n.err != nil {
			errs[i] = n.err
			continue
		}
		wg.Go(func() { errs[i] = op(ctx, n.client.Tenants()) })
	}
	wg.Wait()
	results := make([]nodeResult, 0, len(nodes))
	var failed []string
	for i, n := range nodes {
		r := nodeResult{Node: n.addr}
		if errs[i] != nil {
			r.Error = errs[i].Error()
			failed = append(failed, n.addr)
		}
		results = append(results, r)
	}
	if len(failed) > 0 {
		return results, &BarrierFanOutError{Op: opName, Failed: failed}
	}
	return results, nil
}

// runOnServingNodes sends op to every node in nodes and reports each node's
// answer.
func runOnServingNodes(cmd *cobra.Command, nodes []transport.TenantBarrierNode, opName string, send fanOutSender, op func(context.Context, transport.TenantClient) error) error {
	serving, closeNodes, err := dialServingNodes(cmd, nodes)
	if err != nil {
		return err
	}
	defer closeNodes()
	results, err := send(cmd.Context(), serving, opName, op)
	if renderErr := renderNodeResults(results); renderErr != nil {
		return renderErr
	}
	return err
}

// fanOutSender sends a fan-out operation to the dialled serving nodes:
// sendToServingNodes sends it to every node that answered,
// sendToAllServingNodes only when every node answered.
type fanOutSender func(ctx context.Context, nodes []servingNode, opName string, op func(context.Context, transport.TenantClient) error) ([]nodeResult, error)

// NodeNotSentError reports a node that answered but was not sent an
// all-or-none fan-out because other serving nodes could not be reached.
type NodeNotSentError struct {
	Op        string
	Unreached []string
}

func (e *NodeNotSentError) Error() string {
	return "qrdb: " + e.Op + " was not sent: " + strings.Join(e.Unreached, ", ") + " cannot be reached"
}

// sendToAllServingNodes sends op to every serving node only when every node
// was dialled. A barrier init reaching some nodes leaves the others without
// their key, so the tenant's home group cannot elect a leader and the init
// cannot be rerun on the nodes that took it; refusing it everywhere keeps a
// rerun, once every node is reachable, a whole init.
func sendToAllServingNodes(ctx context.Context, nodes []servingNode, opName string, op func(context.Context, transport.TenantClient) error) ([]nodeResult, error) {
	var unreached []string
	for _, n := range nodes {
		if n.err != nil {
			unreached = append(unreached, n.addr)
		}
	}
	if len(unreached) == 0 {
		return sendToServingNodes(ctx, nodes, opName, op)
	}
	results := make([]nodeResult, 0, len(nodes))
	failed := make([]string, 0, len(nodes))
	for _, n := range nodes {
		err := n.err
		if err == nil {
			err = &NodeNotSentError{Op: opName, Unreached: unreached}
		}
		results = append(results, nodeResult{Node: n.addr, Error: err.Error()})
		failed = append(failed, n.addr)
	}
	return results, &BarrierFanOutError{Op: opName, Failed: failed}
}

// renderNodeResults prints each node's answer.
func renderNodeResults(results []nodeResult) error {
	return renderHolder(results, func() {
		for _, r := range results {
			if r.Error == "" {
				_, _ = fmt.Fprintf(outputWriter, "%s: OK\n", r.Node)
			} else {
				_, _ = fmt.Fprintf(outputWriter, "%s: refused: %s\n", r.Node, r.Error)
			}
		}
	})
}
