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
	"io"
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// startFeatureGate refuses -- with *featuregate.UnsupportedFeatureError --
// to run under a cluster that has activated a feature this binary lacks.
// With a gate it then advertises this binary's features under the node's
// committed system-shard address and starts the gate; the returned closer
// withdraws the advertisement and stops the gate.
func startFeatureGate(ctx context.Context, gate *featuregate.Gate, svc *lifecycle.Node) (io.Closer, error) {
	if gate == nil {
		err := server.WhileSystemShardSettling(ctx, func() error {
			return featuregate.CheckActivated(ctx, svc.KV)
		})
		return nopCloser{}, err
	}
	client, err := newEmbeddedClient(svc.KV, svc.Admin, svc.MVCC, svc.Lease, svc.Watch)
	if err != nil {
		return nil, err
	}
	var adv *cluster.Advertiser
	err = server.WhileSystemShardSettling(ctx, func() error {
		var serr error
		adv, serr = client.StartFeatureGate(ctx, gate, svc.KV)
		return serr
	})
	if err != nil {
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
