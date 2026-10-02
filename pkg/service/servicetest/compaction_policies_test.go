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

package servicetest

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
)

func TestCompactionPolicies_SetReadAndClear(t *testing.T) {
	p := NewCompactionPolicies()
	ctx := context.Background()
	if got, err := p.CompactionPolicy(ctx, "acme", "db"); got != nil || err != nil {
		t.Fatalf("unset policy = %+v, %v; want none", got, err)
	}
	if err := p.SetCompactionPolicy(ctx, "acme", "db", &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 3}); err != nil {
		t.Fatalf("SetCompactionPolicy: %v", err)
	}
	got, err := p.CompactionPolicy(ctx, "acme", "db")
	if err != nil || got == nil || got.RetainRevisions != 3 {
		t.Fatalf("CompactionPolicy = %+v, %v; want the revision policy", got, err)
	}
	if other, _ := p.CompactionPolicy(ctx, "globex", "db"); other != nil {
		t.Fatalf("another tenant's database read acme's policy: %+v", other)
	}
	for _, off := range []*compaction.Policy{{Mode: compaction.Off}, nil} {
		if err := p.SetCompactionPolicy(ctx, "acme", "db", &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 1}); err != nil {
			t.Fatalf("SetCompactionPolicy: %v", err)
		}
		if err := p.SetCompactionPolicy(ctx, "acme", "db", off); err != nil {
			t.Fatalf("SetCompactionPolicy(off): %v", err)
		}
		if got, _ := p.CompactionPolicy(ctx, "acme", "db"); got != nil {
			t.Fatalf("policy after %v = %+v; want none", off, got)
		}
	}
}

func TestCompactionPolicies_RefusesAnInvalidPolicy(t *testing.T) {
	p := NewCompactionPolicies()
	err := p.SetCompactionPolicy(context.Background(), "acme", "db", &compaction.Policy{Mode: compaction.Revision})
	var invalid *compaction.InvalidPolicyError
	if !errors.As(err, &invalid) {
		t.Fatalf("SetCompactionPolicy = %v; want *compaction.InvalidPolicyError", err)
	}
}
