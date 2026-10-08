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

package sdk_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
)

// activations is a featuregate.ActivationKV over a map.
type activations map[string][]byte

func (a activations) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	if _, ok := a[key]; ok {
		return false, nil
	}
	a[key] = value
	return true, nil
}

func (a activations) ListAll(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range a {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (a activations) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return a.ListAll(ctx, prefix)
}

// members is a featuregate.MembershipSource over a fixed system shard.
type members map[uint64]string

func (m members) GroupMembership(uint64) (featuregate.GroupMembership, error) {
	return featuregate.GroupMembership{ConfigChangeID: 1, Replicas: m}, nil
}

func newTestGate(t *testing.T, system members) *featuregate.Gate {
	t.Helper()
	g, err := featuregate.New(featuregate.Config{Self: featuregate.Self{ReplicaID: 1}, SystemShardID: 1, Members: system})
	if err != nil {
		t.Fatalf("featuregate.New: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

// StartFeatureGate refuses before advertising anything when the cluster has
// activated a feature this binary lacks.
func TestClient_StartFeatureGate_RefusesAnUnknownActivatedFeature(t *testing.T) {
	client := newMembershipClient(t)
	defer client.Close()
	kv := activations{featuregate.ActivationPrefix + "future-opcode": []byte(`{}`)}

	_, err := client.StartFeatureGate(context.Background(), newTestGate(t, members{1: "n1"}), kv)
	var unsupported *featuregate.UnsupportedFeatureError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want *featuregate.UnsupportedFeatureError", err)
	}
}

// A node that is not a system-shard member has no committed address to
// advertise.
func TestClient_StartFeatureGate_RequiresSystemMembership(t *testing.T) {
	client := newMembershipClient(t)
	defer client.Close()
	_, err := client.StartFeatureGate(context.Background(), newTestGate(t, members{2: "n2"}), activations{})
	var notMember *featuregate.NotAMemberError
	if !errors.As(err, &notMember) {
		t.Fatalf("err = %v, want *featuregate.NotAMemberError", err)
	}
}

// Without a lease service the advertisement cannot be registered, and the
// gate is not started.
func TestClient_StartFeatureGate_ReportsAFailedAdvertisement(t *testing.T) {
	client := newMembershipClient(t)
	defer client.Close()
	gate := newTestGate(t, members{1: "n1"})
	if _, err := client.StartFeatureGate(context.Background(), gate, activations{}); err == nil {
		t.Fatal("StartFeatureGate succeeded with no lease service to advertise through")
	}
	if gate.Activated(featuregate.GuardedWrites) {
		t.Fatal("the gate was started although its advertisement failed")
	}
}

// NewMembershipStoreAt keeps a registry under its own prefix. Translation
// under featuregate.AdvertisementPrefix itself is TestTranslateMemberEvent's
// table, in package sdk (translateMemberEvent is unexported).
func TestClient_NewMembershipStoreAt(t *testing.T) {
	client := newMembershipClient(t)
	defer client.Close()
	if client.NewMembershipStoreAt(featuregate.AdvertisementPrefix) == nil {
		t.Fatal("NewMembershipStoreAt returned nil")
	}
}
