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

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// featureGateRetryInterval paces startFeatureGate's attempts while the
// system shard has no leader or its ReadIndex is still warming up.
const featureGateRetryInterval = 200 * time.Millisecond

// leaderReporter is the slice of the host startFeatureGate polls.
type leaderReporter interface {
	GetLeaderID(shardID uint64) (uint64, bool, error)
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
		for ctx.Err() == nil {
			if _, ok, err := host.GetLeaderID(shard.ShardKV); err == nil && ok {
				// The attempt's deadline bounds the start's reads, which a
				// leader not yet committed in its term drops unanswered.
				actx, cancel := context.WithTimeout(ctx, server.StartupReadAttemptTimeout)
				adv, err := client.StartFeatureGate(actx, gate, activations, cluster.WithAdvertiseSealProbe(n))
				cancel()
				if err == nil {
					n.featureAdv.Store(adv)
					return
				}
				var unsupported *featuregate.UnsupportedFeatureError
				if errors.As(err, &unsupported) {
					logger := n.logger
					if logger == nil {
						logger = slog.Default()
					}
					logger.Error("feature gate: this binary cannot run in this cluster", slog.Any("error", err))
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
