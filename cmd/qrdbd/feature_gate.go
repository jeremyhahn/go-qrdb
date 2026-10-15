// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// startFeatureGate refuses -- with *featuregate.UnsupportedFeatureError --
// to run under a cluster that has activated a feature this binary lacks.
// With a gate it then advertises this binary's features under the node's
// committed system-shard address and starts the gate; the returned closer
// withdraws the advertisement and stops the gate.
//
// Self-advertisement is a leased membership record (cluster.Advertiser),
// which some state machine types -- "memory", "disk" -- do not implement
// (they return *statemachine.ServiceNotConfiguredError{Service: "lease"},
// which the embedded transport reports as
// *transport.CapabilityUnsupportedError: see that type's own doc comment,
// "a consumer that wants to degrade gracefully ... detects it with
// errors.As"). client.StartFeatureGate's compatibility check
// (gate.CheckActivated) runs and must already have passed before
// self-advertisement is ever attempted, so a node on such a state machine
// boots and serves without advertising its own features or watching for
// newly-activated ones -- exactly as if no gate had been built at all --
// rather than exiting over an accelerator this build never required.
func startFeatureGate(ctx context.Context, gate *featuregate.Gate, svc *lifecycle.Node, nodeSeal cluster.SealProbe) (io.Closer, error) {
	if gate == nil {
		err := server.WhileSystemShardSettling(ctx, "feature gate activation check", func(actx context.Context) error {
			return featuregate.CheckActivated(actx, svc.KV)
		})
		return nopCloser{}, err
	}
	client, err := newEmbeddedClient(svc.KV, svc.Admin, svc.MVCC, svc.Lease, svc.Watch)
	if err != nil {
		return nil, err
	}
	var adv *cluster.Advertiser
	// The attempt's deadline bounds the start's reads, which a leader not yet
	// committed in its term drops unanswered; the advertisement and the gate
	// it starts run on until featureGateCloser closes them.
	err = server.WhileSystemShardSettling(ctx, "feature gate start", func(actx context.Context) error {
		var serr error
		adv, serr = client.StartFeatureGate(actx, gate, svc.KV, cluster.WithAdvertiseSealProbe(nodeSeal))
		return serr
	})
	if err != nil {
		var unsupported *transport.CapabilityUnsupportedError
		if errors.As(err, &unsupported) {
			slog.Warn("feature-gate self-advertisement unavailable on this state machine; "+
				"booting without it",
				slog.String("reason", unsupported.Message))
			return nopCloser{}, nil
		}
		return nil, err
	}
	return featureGateCloser{adv: adv, gate: gate}, nil
}

// withdrawFeatures withdraws this node's feature advertisement and stops the
// gate while the host and services are still up, so the lease is revoked at
// once rather than left to expire. Shutdown and the migration recycle call it
// before tearing the data plane down; closing again later is a no-op.
func withdrawFeatures(gateCloser io.Closer) {
	if err := gateCloser.Close(); err != nil {
		slog.Error("feature advertisement withdrawal failed", slog.Any("error", err))
	}
}

// featureGateCloser withdraws the advertisement, then stops the gate. Both
// halves are idempotent, so a second Close is a no-op.
type featureGateCloser struct {
	adv  *cluster.Advertiser
	gate *featuregate.Gate
}

func (c featureGateCloser) Close() error {
	err := c.adv.Close()
	if gerr := c.gate.Close(); err == nil {
		err = gerr
	}
	return err
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
