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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// guardedFactorTransport is an embedded transport over the real factor
// service on a store with guarded writes, so a conditional add is decided by
// the code that decides it in production.
func guardedFactorTransport(t *testing.T) *Transport {
	t.Helper()
	svc, err := service.NewUserAuthFactorService(service.UserAuthFactorServiceConfig{KV: kvstore.NewMemoryKVStore()})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return &Transport{userAuthFactorSvc: svc}
}

func TestAddAuthFactor_conditionalClaimThenList(t *testing.T) {
	tr := guardedFactorTransport(t)
	ctx := context.Background()
	add := func(cred string) (*transport.AuthFactorAddResponse, error) {
		return tr.AddAuthFactor(ctx, &transport.AuthFactorAddRequest{
			UserID: 42, Type: "piv", Label: "card", Credential: []byte(cred), IfNoActiveType: true,
		})
	}

	first, err := add("cred-1")
	if err != nil || first.FactorSetVersion != 1 || first.Factor.ID == "" {
		t.Fatalf("claim = %+v, %v; want the record at factor-set version 1", first, err)
	}
	_, err = add("cred-2")
	refusal, ok := errors.AsType[*auth.FactorConditionFailedError](err)
	if !ok || refusal.ExistingFactorID != first.Factor.ID || refusal.Condition != auth.FactorConditionNoActiveOfType {
		t.Fatalf("second claim err = %v, want a no-active-of-type refusal naming %s", err, first.Factor.ID)
	}

	list, err := tr.ListAuthFactors(ctx, &transport.AuthFactorListRequest{UserID: 42})
	if err != nil {
		t.Fatalf("ListAuthFactors: %v", err)
	}
	if list.FactorSetVersion != 1 || list.Total != 1 || len(list.Factors) != 1 || list.Factors[0].ID != first.Factor.ID {
		t.Fatalf("list = %+v, want the one claimed factor at version 1", list)
	}
}

func TestAddAuthFactor_expectedVersionAgainstTheListedOne(t *testing.T) {
	tr := guardedFactorTransport(t)
	ctx := context.Background()
	add := func(cred string, expected uint64) (*transport.AuthFactorAddResponse, error) {
		return tr.AddAuthFactor(ctx, &transport.AuthFactorAddRequest{
			UserID: 42, Type: "fido2", Label: cred, Credential: []byte(cred), ExpectedFactorSetVersion: &expected,
		})
	}

	if _, err := add("cred-1", 0); err != nil {
		t.Fatalf("add at version 0: %v", err)
	}
	_, err := add("cred-2", 0)
	if refusal, ok := errors.AsType[*auth.FactorConditionFailedError](err); !ok || refusal.CurrentVersion != 1 {
		t.Fatalf("stale add err = %v, want a refusal reporting version 1", err)
	}
	list, err := tr.ListAuthFactors(ctx, &transport.AuthFactorListRequest{UserID: 42})
	if err != nil {
		t.Fatalf("ListAuthFactors: %v", err)
	}
	resp, err := add("cred-3", list.FactorSetVersion)
	if err != nil || resp.FactorSetVersion != 2 {
		t.Fatalf("add at the listed version = %+v, %v; want factor-set version 2", resp, err)
	}
}

func TestListAuthFactors_serviceErrorFailsTheCall(t *testing.T) {
	tr := guardedFactorTransport(t)

	if resp, err := tr.ListAuthFactors(context.Background(), &transport.AuthFactorListRequest{}); err == nil {
		t.Fatalf("list for user 0 = %+v, want an error", resp)
	}
}

func TestRotateAuthFactor_refusesAnAddCondition(t *testing.T) {
	tr := guardedFactorTransport(t)
	ctx := context.Background()
	old, err := tr.AddAuthFactor(ctx, &transport.AuthFactorAddRequest{UserID: 42, Type: "piv", Label: "old", Credential: []byte("old")})
	if err != nil {
		t.Fatalf("AddAuthFactor: %v", err)
	}
	before, err := tr.ListAuthFactors(ctx, &transport.AuthFactorListRequest{UserID: 42})
	if err != nil {
		t.Fatalf("ListAuthFactors: %v", err)
	}

	version := before.FactorSetVersion
	for name, newFactor := range map[string]transport.AuthFactorAddRequest{
		"if-no-active-type": {UserID: 42, Type: "piv", Label: "new", Credential: []byte("new"), IfNoActiveType: true},
		"expected-version":  {UserID: 42, Type: "piv", Label: "new", Credential: []byte("new"), ExpectedFactorSetVersion: &version},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tr.RotateAuthFactor(ctx, &transport.AuthFactorRotateRequest{OldFactorID: old.Factor.ID, NewFactor: newFactor})
			if _, ok := errors.AsType[*auth.InvalidFactorError](err); !ok {
				t.Fatalf("err = %v, want an InvalidFactorError", err)
			}
			list, err := tr.ListAuthFactors(ctx, &transport.AuthFactorListRequest{UserID: 42})
			if err != nil || list.FactorSetVersion != before.FactorSetVersion || list.Total != 1 || list.Factors[0].ID != old.Factor.ID {
				t.Fatalf("after the refused rotation list = %+v, %v; want only the old factor, set untouched", list, err)
			}
		})
	}
}

func TestDomainToUserAuthRecord_Nil(t *testing.T) {
	if got := domainToUserAuthRecord(nil); got.ID != "" {
		t.Errorf("domainToUserAuthRecord(nil).ID = %q, want empty", got.ID)
	}
}
