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

package sdk

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
)

// StartFeatureGate brings a node's feature gate up over this client's
// system store. It first refuses -- with *featuregate.UnsupportedFeatureError,
// before advertising anything -- when the cluster has activated a feature this
// binary lacks; then it advertises this binary's features under a lease
// (featuregate.AdvertisementPrefix, keyed by the node's system replica ID
// and carrying its committed raft address, gate.AdvertisedSelf) and starts
// gate.
//
// The returned Advertiser keeps the advertisement alive; closing it
// withdraws the advertisement. Close gate separately.
func (c *Client) StartFeatureGate(ctx context.Context, gate *featuregate.Gate, activations featuregate.ActivationKV) (*cluster.Advertiser, error) {
	if err := gate.CheckActivated(ctx, activations); err != nil {
		return nil, err
	}
	self, err := gate.AdvertisedSelf()
	if err != nil {
		return nil, err
	}
	adv, err := cluster.NewAdvertiser(
		c.NewMembershipStoreAt(featuregate.AdvertisementPrefix),
		cluster.Member{NodeID: self.ReplicaID, Address: self.Address, Features: featuregate.SupportedNames()},
		cluster.AdvertiseConfig{},
	)
	if err != nil {
		return nil, err
	}
	if err := adv.Start(ctx); err != nil {
		return nil, err
	}
	// The gate follows the feature advertisements and, for a node that
	// joins through dynamic membership before it can advertise, the member
	// registrations, which carry the features the joining binary declares.
	if err := gate.Start(ctx, activations,
		c.NewMembershipStoreAt(featuregate.AdvertisementPrefix),
		c.NewMembershipStore()); err != nil {
		_ = adv.Close()
		return nil, err
	}
	return adv, nil
}
