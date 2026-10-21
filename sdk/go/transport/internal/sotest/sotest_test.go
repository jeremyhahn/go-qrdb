// Copyright 2026 Jeremy Hahn
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

package sotest

import (
	"context"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// silentError is an error whose message is empty, the case describe exists
// for.
type silentError struct{}

func (silentError) Error() string { return "" }

func TestDescribe_NilSaysNoError(t *testing.T) {
	if got := describe(nil); got != "no error" {
		t.Fatalf("describe(nil) = %q, want %q", got, "no error")
	}
}

func TestDescribe_EmptyMessageStillNamesTheType(t *testing.T) {
	got := describe(silentError{})
	if want := `sotest.silentError ""`; got != want {
		t.Fatalf("describe(silentError{}) = %q, want %q", got, want)
	}
}

func TestDescribe_TypedErrorCarriesTypeAndMessage(t *testing.T) {
	err := &transport.SOPINConflictError{Method: "BarrierGenerateRecoveryKeys"}
	got := describe(err)
	if !strings.HasPrefix(got, "*transport.SOPINConflictError ") || !strings.Contains(got, err.Error()) {
		t.Fatalf("describe(%T) = %q, want its type and quoted message %q", err, got, err.Error())
	}
}

func TestFixtureReached_FreshFixtureReportsNothing(t *testing.T) {
	f := NewFixture(t)
	want := "service saw rekeys=0 recovery generations=0 recovery deletes=0 seals=0 shamir inits=0 ceremony SO PINs=0"
	if got := f.reached(); got != want {
		t.Fatalf("reached() = %q, want %q", got, want)
	}
}

func TestFixtureReached_CountsWhatReachedTheDoubles(t *testing.T) {
	f := NewFixture(t)
	ctx := context.Background()
	if _, err := f.Barrier.BarrierGenerateRecoveryKeys(ctx, 2, ""); err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys: %v", err)
	}
	if err := f.Barrier.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
	if _, err := f.Ceremony.CeremonySignCSR(ctx, "officer", PIN, "csr", "officer"); err != nil {
		t.Fatalf("CeremonySignCSR: %v", err)
	}
	want := "service saw rekeys=0 recovery generations=1 recovery deletes=0 seals=1 shamir inits=0 ceremony SO PINs=1"
	if got := f.reached(); got != want {
		t.Fatalf("reached() = %q, want %q", got, want)
	}
}
