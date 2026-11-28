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

package clusterkeys_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterkeys"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/release"
)

// followerMeta is the delivery metadata addressed to the replica "follower".
func followerMeta() keydelivery.Meta {
	return keydelivery.Meta{
		TenantID:     "t",
		Database:     "db",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower",
		Mode:         keydelivery.ModeSelfManaged,
	}
}

// TestSender_SealFor_ResolveURLFails verifies that sealing for a replica whose
// admin URL cannot be resolved fails with ErrResolveURL and fetches nothing.
func TestSender_SealFor_ResolveURLFails(t *testing.T) {
	ca := newTestCA(t)
	resolver := &staticURLResolver{raftAddr: "other-addr", adminURL: "http://irrelevant"}
	sender, err := clusterkeys.NewSender(resolver, http.DefaultClient, ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	replica := placement.Replica{ReplicaID: 2, NodeID: "follower", RaftAddr: "follower:9000"}

	env, err := sender.SealFor(context.Background(), replica, followerMeta(), []byte("key"))
	if !errors.Is(err, clusterkeys.ErrResolveURL) {
		t.Fatalf("SealFor = %v, want ErrResolveURL", err)
	}
	if env != nil {
		t.Fatal("SealFor returned an envelope for an unresolvable replica")
	}
}

// TestSender_Deliver_NodeKeyFetchFails verifies that a replica whose node key
// cannot be fetched is never sent a delivery: Deliver fails before any POST.
func TestSender_Deliver_NodeKeyFetchFails(t *testing.T) {
	ca := newTestCA(t)
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posted = true
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	resolver := &staticURLResolver{raftAddr: "follower:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	replica := placement.Replica{ReplicaID: 2, NodeID: "follower", RaftAddr: "follower:9000"}

	if err := sender.Deliver(context.Background(), replica, followerMeta(), []byte("key")); err == nil {
		t.Fatal("Deliver succeeded although the node key could not be fetched")
	}
	if posted {
		t.Fatal("Deliver posted an envelope without the recipient's node key")
	}
}

// TestSender_Deliver_PostTransportFails verifies that a delivery whose POST
// never gets an answer -- the connection is dropped -- fails with ErrDelivery.
func TestSender_Deliver_PostTransportFails(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/cluster/nodekey" {
			w.Header().Set("Content-Type", "application/x-pem-file")
			_, _ = w.Write(follower.pem)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	resolver := &staticURLResolver{raftAddr: "follower:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	replica := placement.Replica{ReplicaID: 2, NodeID: "follower", RaftAddr: "follower:9000"}

	err = sender.Deliver(context.Background(), replica, followerMeta(), []byte("key"))
	if !errors.Is(err, clusterkeys.ErrDelivery) {
		t.Fatalf("Deliver = %v, want ErrDelivery for a dropped connection", err)
	}
}

// conflictReceiver serves the follower's node key and answers every
// key-delivery POST with 409 and the given JSON body.
func conflictReceiver(t *testing.T, body string) (*clusterkeys.Sender, placement.Replica) {
	t.Helper()
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/cluster/nodekey" {
			w.Header().Set("Content-Type", "application/x-pem-file")
			_, _ = w.Write(follower.pem)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	resolver := &staticURLResolver{raftAddr: "follower:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	return sender, placement.Replica{ReplicaID: 2, NodeID: "follower", RaftAddr: "follower:9000"}
}

// TestSender_Deliver_ReleaseGapIsTyped pins SPEC rolling upgrades clause
// (3) on the sender: a receiver refusing on a release gap (409 carrying its
// release) surfaces as a *release.GapError naming both releases, still
// ErrDelivery.
func TestSender_Deliver_ReleaseGapIsTyped(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.58.0-alpha"))
	sender, replica := conflictReceiver(t, `{"error":"release gap","release_version":"0.60.0-alpha"}`)

	err := sender.Deliver(context.Background(), replica, followerMeta(), []byte("key"))
	want := release.GapError{Channel: "key-delivery", Local: "0.58.0-alpha", Peer: "0.60.0-alpha"}
	var gap *release.GapError
	if !errors.As(err, &gap) || *gap != want {
		t.Fatalf("Deliver across a release gap = %v, want %+v", err, want)
	}
	if !errors.Is(err, clusterkeys.ErrDelivery) {
		t.Fatalf("Deliver across a release gap = %v, want ErrDelivery too", err)
	}
}

// TestSender_Deliver_PlainConflictIsUntyped pins that a 409 without a
// release is a plain ErrDelivery.
func TestSender_Deliver_PlainConflictIsUntyped(t *testing.T) {
	sender, replica := conflictReceiver(t, `{"error":"conflict"}`)

	err := sender.Deliver(context.Background(), replica, followerMeta(), []byte("key"))
	if !errors.Is(err, clusterkeys.ErrDelivery) {
		t.Fatalf("Deliver = %v, want ErrDelivery", err)
	}
	var gap *release.GapError
	if errors.As(err, &gap) {
		t.Fatalf("Deliver = %v, want no *release.GapError without a release", err)
	}
}
