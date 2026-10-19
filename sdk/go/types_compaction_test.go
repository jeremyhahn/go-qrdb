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
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// compactionAdminService is an AdminServicer double whose node compaction
// policy behaves as service.AdminService's does: SetNodeCompaction refuses an
// invalid policy before storing anything and stores the normalized policy, so
// an off policy and an unset one read back alike, as nil.
type compactionAdminService struct {
	mockAdminService
	mu     sync.Mutex
	policy *compaction.Policy
}

var _ sdk.AdminServicer = (*compactionAdminService)(nil)

func (m *compactionAdminService) NodeCompaction(_ context.Context) (*compaction.Policy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.policy.Normalized(), nil
}

func (m *compactionAdminService) SetNodeCompaction(_ context.Context, policy *compaction.Policy) error {
	if policy != nil {
		if err := policy.Validate(); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policy = policy.Normalized()
	return nil
}

func TestCompactionPolicy_AssignsToAndFromPkgType(t *testing.T) {
	pkgPolicy := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 10}

	var sdkPolicy *sdk.CompactionPolicy = pkgPolicy
	if sdkPolicy != pkgPolicy {
		t.Fatal("sdk.CompactionPolicy is not the same pointer as compaction.Policy")
	}

	var back *compaction.Policy = sdkPolicy
	if back.Mode != compaction.Revision || back.RetainRevisions != 10 {
		t.Fatalf("round trip changed the policy: %+v", back)
	}
}

func TestCompactionMode_ConstantsMatchPkg(t *testing.T) {
	cases := map[sdk.CompactionMode]compaction.Mode{
		sdk.CompactionOff:      compaction.Off,
		sdk.CompactionRevision: compaction.Revision,
		sdk.CompactionPeriod:   compaction.Period,
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("sdk mode %q != compaction mode %q", got, want)
		}
	}
	if sdk.CompactionMinRetainPeriod != compaction.MinRetainPeriod {
		t.Errorf("CompactionMinRetainPeriod = %v, want %v", sdk.CompactionMinRetainPeriod, compaction.MinRetainPeriod)
	}
}

func TestCompactionPolicy_SDKBuiltRoundTripsThroughAdminServicer(t *testing.T) {
	ctx := context.Background()
	var admin sdk.AdminServicer = &compactionAdminService{}

	policies := []*sdk.CompactionPolicy{
		{Mode: sdk.CompactionRevision, RetainRevisions: 100},
		{Mode: sdk.CompactionPeriod, RetainPeriod: (2 * time.Hour).String()},
	}
	for _, want := range policies {
		if err := admin.SetNodeCompaction(ctx, want); err != nil {
			t.Fatalf("SetNodeCompaction(%+v): %v", want, err)
		}
		got, err := admin.NodeCompaction(ctx)
		if err != nil {
			t.Fatalf("NodeCompaction: %v", err)
		}
		if got == nil || *got != *want {
			t.Fatalf("NodeCompaction = %+v, want %+v", got, want)
		}
	}
}

func TestCompactionPolicy_SDKBuiltOffReadsBackNil(t *testing.T) {
	ctx := context.Background()
	var admin sdk.AdminServicer = &compactionAdminService{}

	if err := admin.SetNodeCompaction(ctx, &sdk.CompactionPolicy{Mode: sdk.CompactionRevision, RetainRevisions: 5}); err != nil {
		t.Fatalf("SetNodeCompaction: %v", err)
	}
	if err := admin.SetNodeCompaction(ctx, &sdk.CompactionPolicy{Mode: sdk.CompactionOff}); err != nil {
		t.Fatalf("SetNodeCompaction(off): %v", err)
	}
	got, err := admin.NodeCompaction(ctx)
	if err != nil {
		t.Fatalf("NodeCompaction: %v", err)
	}
	if got != nil {
		t.Fatalf("an off policy read back as %+v, want nil", got)
	}
}

func TestCompactionInvalidPolicyError_FromSDKBuiltPolicy(t *testing.T) {
	invalid := map[string]*sdk.CompactionPolicy{
		"unknown mode":         {Mode: sdk.CompactionMode("weekly")},
		"period below minimum": {Mode: sdk.CompactionPeriod, RetainPeriod: (sdk.CompactionMinRetainPeriod / 2).String()},
	}
	for name, p := range invalid {
		var target *sdk.CompactionInvalidPolicyError
		if err := p.Validate(); !errors.As(err, &target) {
			t.Errorf("%s: Validate() = %v, want *sdk.CompactionInvalidPolicyError", name, err)
		}
	}
}

func TestCompactionInvalidPolicyError_RefusedByAdminServicer(t *testing.T) {
	ctx := context.Background()
	stored := &sdk.CompactionPolicy{Mode: sdk.CompactionRevision, RetainRevisions: 3}
	var admin sdk.AdminServicer = &compactionAdminService{}
	if err := admin.SetNodeCompaction(ctx, stored); err != nil {
		t.Fatalf("SetNodeCompaction: %v", err)
	}

	err := admin.SetNodeCompaction(ctx, &sdk.CompactionPolicy{Mode: sdk.CompactionRevision})
	var target *sdk.CompactionInvalidPolicyError
	if !errors.As(err, &target) {
		t.Fatalf("SetNodeCompaction(invalid) = %v, want *sdk.CompactionInvalidPolicyError", err)
	}

	got, err := admin.NodeCompaction(ctx)
	if err != nil {
		t.Fatalf("NodeCompaction: %v", err)
	}
	if got == nil || *got != *stored {
		t.Fatalf("a refused policy replaced the stored one: got %+v, want %+v", got, stored)
	}
}
