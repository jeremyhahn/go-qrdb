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

package service

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// wholeNodeFixture is an initialized node barrier with one system-managed
// (auto-unseal) tenant and one self-managed tenant, both unsealed, and the
// node's (inactive) cluster sealer with its stream registry.
type wholeNodeFixture struct {
	reg      *seal.BarrierRegistry
	deferred *clusterseal.Deferred
	svc      BarrierServicer
}

const wholeNodePass = "whole-node-pass"

func newWholeNodeFixture(t *testing.T) *wholeNodeFixture {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	strategy := seal.NewDeferredSoftwareStrategy()
	sys, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "sys/root"}, strategy)
	if err != nil {
		t.Fatal(err)
	}
	if err := sys.Initialize(ctx, seal.MustCredentials([]byte(wholeNodePass))); err != nil {
		t.Fatal(err)
	}
	reg, err := seal.NewBarrierRegistry(logger, sys)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	if _, err := reg.RegisterTenant(ctx, "auto", seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "tenants/auto/root"}, seal.NewSystemBarrierStrategy(sys)); err != nil {
		t.Fatal(err)
	}
	if err := reg.InitializeTenant(ctx, "auto", seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.RegisterTenantDefault("self"); err != nil {
		t.Fatal(err)
	}
	if err := reg.InitializeTenant(ctx, "self", seal.MustCredentials([]byte("self-pass"))); err != nil {
		t.Fatal(err)
	}
	deferred := clusterseal.NewDeferred()
	svc := NewBarrierService(sys, WithClusterSealer(deferred), WithBarrierRegistry(reg))
	return &wholeNodeFixture{reg: reg, deferred: deferred, svc: svc}
}

// requireAllSealed fails unless the node barrier and every tenant barrier are
// sealed with their keys -- the epoch barrier's root key and DEKs -- wiped.
func (f *wholeNodeFixture) requireAllSealed(t *testing.T) {
	t.Helper()
	if !f.reg.SystemBarrier().IsSealed() {
		t.Fatal("the system barrier is unsealed")
	}
	ids := f.reg.ListTenants()
	if len(ids) != 2 {
		t.Fatalf("tenants = %v, want auto and self", ids)
	}
	for _, id := range ids {
		tb, err := f.reg.GetTenant(id)
		if err != nil {
			t.Fatal(err)
		}
		if !tb.IsSealed() {
			t.Fatalf("tenant %s is unsealed after the system seal", id)
		}
		if !tb.Barrier().EpochBarrier().IsSealed() {
			t.Fatalf("tenant %s still holds its keys in memory after the system seal", id)
		}
	}
}

// TestBarrierSeal_SealsTheWholeNode pins the owner's ruling: a system-barrier
// seal seals every tenant barrier on the node and wipes their keys, ends the
// tenants' open streams, and on unseal brings the system-managed tenants back
// while the self-managed one stays sealed for its own admin. The Shamir seal
// vote's commit seals through the same function (BarrierService.seal) as the
// single-admin seal exercised here.
func TestBarrierSeal_SealsTheWholeNode(t *testing.T) {
	f := newWholeNodeFixture(t)
	ctx := context.Background()
	streams := f.deferred.Streams()
	autoStream, err := streams.SubscribeTenantServing("auto", nil)
	if err != nil {
		t.Fatal(err)
	}
	selfStream, err := streams.SubscribeServing("self", "ledger", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.svc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
	f.requireAllSealed(t)
	for name, sub := range map[string]*clusterseal.Subscription{"auto": autoStream, "self": selfStream} {
		select {
		case <-sub.Done():
		default:
			t.Fatalf("tenant %s's open stream outlived the system seal", name)
		}
	}

	if err := f.svc.BarrierUnseal(ctx, []byte(wholeNodePass)); err != nil {
		t.Fatalf("BarrierUnseal: %v", err)
	}
	auto, _ := f.reg.GetTenant("auto")
	self, _ := f.reg.GetTenant("self")
	if auto.IsSealed() {
		t.Fatal("the system-managed tenant stayed sealed after the system unseal")
	}
	if !self.IsSealed() {
		t.Fatal("the self-managed tenant was unsealed by the system unseal")
	}
}
