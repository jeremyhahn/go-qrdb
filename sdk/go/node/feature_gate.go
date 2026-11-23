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

package node

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/release"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// featureGateRetryInterval paces startFeatureGate's attempts while the
// system shard has no leader or its ReadIndex is still warming up.
const featureGateRetryInterval = 200 * time.Millisecond

// leaderReporter is the slice of the host startFeatureGate polls.
type leaderReporter interface {
	LeaderID(shardID uint64) (uint64, bool, error)
}

// startFeatureGate brings the node's feature gate up in the background: once
// the system shard has a leader it checks the cluster's activated features,
// advertises this binary's, and starts the gate. It runs in the background
// because a clustered node is built before its peers, and waiting here for a
// quorum would deadlock a cluster whose members start one after another.
// Until it succeeds the gate enables guarded writes only for groups whose
// every replica is this node, so a mixed group is never sent one.
//
// A cluster that has activated a feature this binary lacks is logged as an
// error and the gate is left down: this node never advertises, so no group
// holding it is ever enabled by a gate that is not already activated.
func (n *Node) startFeatureGate(host leaderReporter, activations featuregate.ActivationKV) {
	gate := n.featureGate
	client := n.featureClient.Load()
	if gate == nil || client == nil || activations == nil {
		return
	}
	ctx := n.background()
	n.featureWG.Add(1)
	go func() {
		defer n.featureWG.Done()
		// leaseless is set once the state machine is found to have no
		// leases: the advertisement, and so Gate.Start, cannot run, but the
		// release history needs no lease, so the restart release check and
		// record still run on their own (as the daemon does).
		leaseless := false
		for ctx.Err() == nil {
			if _, ok, err := host.LeaderID(shard.ShardKV); err == nil && ok {
				// The attempt's deadline bounds the start's reads, which a
				// leader not yet committed in its term drops unanswered.
				actx, cancel := context.WithTimeout(ctx, server.StartupReadAttemptTimeout)
				var adv *cluster.Advertiser
				if !leaseless {
					adv, err = client.StartFeatureGate(actx, gate, activations, cluster.WithAdvertiseSealProbe(n))
					var unsupported *transport.CapabilityUnsupportedError
					if errors.As(err, &unsupported) {
						n.featureLogger().Warn("feature-gate self-advertisement unavailable on this state machine; "+
							"enforcing the release history without it", slog.String("reason", unsupported.Message))
						leaseless = true
					}
				}
				if leaseless {
					err = gate.EnforceRelease(actx, activations)
				}
				cancel()
				if err == nil {
					if adv != nil {
						n.featureAdv.Store(adv)
					}
					return
				}
				if featureGateRefused(err) {
					n.featureLogger().Error("feature gate: this binary cannot run in this cluster; "+
						"quarantining the node", slog.Any("error", err))
					n.quarantine(host, err)
					return
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(featureGateRetryInterval):
			}
		}
	}()
}

// nodeRefusal boxes the terminal release refusal kept by Node.quarantine.
type nodeRefusal struct{ err error }

// hostCloser is the part of the Raft host the quarantine closes.
type hostCloser interface{ Close() error }

// quarantine takes a node the release check refused off the cluster, the
// embedded equivalent of the daemon exiting on the same refusal (SPEC
// rolling upgrades clause (3)): it keeps the refusal, withdraws the member
// record and the feature advertisement, and closes the Raft host so the node
// no longer votes, replicates or serves. The node's seal probe
// (refusalProbe) then refuses every data-plane call. Node.Close stays safe
// afterwards: Host.Close is idempotent and the advertisers are swapped out.
func (n *Node) quarantine(host leaderReporter, err error) {
	n.refusal.Store(&nodeRefusal{err: err})
	if adv := n.memberAdv.Swap(nil); adv != nil {
		_ = adv.Close()
	}
	if adv := n.featureAdv.Swap(nil); adv != nil {
		_ = adv.Close()
	}
	if c, ok := host.(hostCloser); ok {
		if cerr := c.Close(); cerr != nil {
			n.featureLogger().Error("feature gate: closing the refused node's Raft host", slog.Any("error", cerr))
		}
	}
}

// Refusal returns the release refusal that quarantined this node, or nil.
// While it is non-nil the node takes no part in Raft and serves nothing.
func (n *Node) Refusal() error {
	if r := n.refusal.Load(); r != nil {
		return r.err
	}
	return nil
}

// refusalProbe wraps the node's seal probe so a quarantined node reports
// itself sealed and the embedded transport refuses every data-plane call.
func (n *Node) refusalProbe(inner embedded.SealProbe) quarantineProbe {
	return quarantineProbe{n: n, inner: inner}
}

// quarantineProbe is the seal probe returned by Node.refusalProbe.
type quarantineProbe struct {
	n     *Node
	inner embedded.SealProbe
}

// SealState implements sealedwindow.StateProbe.
func (p quarantineProbe) SealState() (sealed, unconfirmed bool) {
	if p.n.Refusal() != nil {
		return true, false
	}
	return sealedwindow.Sample(p.inner)
}

// IsSealed implements embedded.SealProbe.
func (p quarantineProbe) IsSealed() bool {
	sealed, _ := p.SealState()
	return sealed
}

// featureLogger is the node's logger, or the default one.
func (n *Node) featureLogger() *slog.Logger {
	if n.logger == nil {
		return slog.Default()
	}
	return n.logger
}

// featureGateRefused reports whether a feature gate start failed because
// this binary cannot run in the cluster -- an activated feature it lacks
// (*featuregate.UnsupportedFeatureError), or a committed member restarted
// across a release gap (*release.GapError). Neither passes on a retry, so
// startFeatureGate stops and leaves the refusal, which the gate keeps in
// Gate.Refusal, to the node's health. Any other failure is retried.
func featureGateRefused(err error) bool {
	var unsupported *featuregate.UnsupportedFeatureError
	var gap *release.GapError
	return errors.As(err, &unsupported) || errors.As(err, &gap)
}

// stopFeatureGate waits out startFeatureGate -- Close cancels its context
// first -- then withdraws the advertisement and stops the gate.
func (n *Node) stopFeatureGate() error {
	n.featureWG.Wait()
	var err error
	if adv := n.featureAdv.Swap(nil); adv != nil {
		err = adv.Close()
	}
	if n.featureGate != nil {
		if gerr := n.featureGate.Close(); err == nil {
			err = gerr
		}
	}
	return err
}

// newConnectedEmbeddedClient builds and connects an in-process client over
// svcNode with opts.
func newConnectedEmbeddedClient(ctx context.Context, svcNode *lifecycle.Node, opts []embedded.Option) (*sdk.Client, error) {
	tp, err := embedded.New(svcNode.KV, svcNode.Admin, opts...)
	if err != nil {
		return nil, &ClientCreateError{Err: err}
	}
	client, err := sdk.NewClient(tp)
	if err != nil {
		return nil, &ClientCreateError{Err: err}
	}
	if err := client.Connect(ctx); err != nil {
		_ = client.Close()
		return nil, &ClientCreateError{Err: err}
	}
	return client, nil
}

// closeFeatureClient retracts and closes the feature gate's client.
func (n *Node) closeFeatureClient() {
	if client := n.featureClient.Swap(nil); client != nil {
		_ = client.Close()
	}
}
