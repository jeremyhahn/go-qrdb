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
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// Compile-time: the conditional-add surface crosses the SDK boundary as
// aliases of the canonical declarations. A copied type would fail these
// assignments.
var (
	_ *auth.FactorConditionFailedError = (*sdk.FactorConditionFailedError)(nil)
	_ *auth.FactorAddCondition         = (*sdk.FactorAddCondition)(nil)
	_ auth.FactorConditionKind         = sdk.FactorConditionKind("")
)

func TestSDK_FactorConditionFailedMatchesThroughTheSDK(t *testing.T) {
	refusal := &auth.FactorConditionFailedError{UserID: 42, Type: auth.AuthFactorPIV, Condition: auth.FactorConditionNoActiveOfType, CurrentVersion: 2}
	err := fmt.Errorf("add factor: %w", refusal)

	if !errors.Is(err, sdk.ErrFactorConditionFailed) {
		t.Fatalf("errors.Is(%v, sdk.ErrFactorConditionFailed) = false", err)
	}
	got, ok := errors.AsType[*sdk.FactorConditionFailedError](err)
	if !ok || got != refusal {
		t.Fatalf("errors.As = %v, %v; want the wrapped refusal", got, ok)
	}
	if errors.Is(sdk.ErrFactorAlreadyExists, sdk.ErrFactorConditionFailed) {
		t.Fatal("a duplicate credential must not read as a failed condition")
	}
}

func TestSDK_FactorConditionKindsAreTheCanonicalOnes(t *testing.T) {
	if sdk.FactorConditionNoActiveOfType != auth.FactorConditionNoActiveOfType {
		t.Errorf("no-active kind = %q, want %q", sdk.FactorConditionNoActiveOfType, auth.FactorConditionNoActiveOfType)
	}
	if sdk.FactorConditionExpectedVersion != auth.FactorConditionExpectedVersion {
		t.Errorf("expected-version kind = %q, want %q", sdk.FactorConditionExpectedVersion, auth.FactorConditionExpectedVersion)
	}
	if sdk.FactorConditionNoActiveOfType == sdk.FactorConditionExpectedVersion {
		t.Error("the two condition kinds share a value")
	}
}
