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
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
)

// fullInitializeOptions builds the options through the SDK alias with SOPin,
// UserPin, CredentialSealStrategy, Threshold, Policy and Officers set, so a field renamed or removed
// in pkg/ceremony fails this package's build rather than a consumer's.
func fullInitializeOptions() InitializeOptions {
	return InitializeOptions{
		SOPin:                  "so-pin-1234",
		UserPin:                "user-pin-5678",
		CredentialSealStrategy: "passphrase",
		Threshold:              2,
		Policy:                 "designated",
		Officers: []OfficerConfig{
			{Username: "alice"},
			{Username: "bob"},
			{Username: "carol"},
		},
	}
}

// TestInitializeOptions_IsAnAliasNotACopy pins the alias to pkg/ceremony's
// type: a value built through the SDK is the value the ceremony takes.
func TestInitializeOptions_IsAnAliasNotACopy(t *testing.T) {
	if got, want := reflect.TypeFor[InitializeOptions](), reflect.TypeFor[ceremony.InitializeOptions](); got != want {
		t.Fatalf("InitializeOptions is %v, want an alias of %v", got, want)
	}
}

// TestInitializeOptions_EveryFieldThroughTheSDK checks every field set
// through the alias survives on pkg/ceremony's type.
func TestInitializeOptions_EveryFieldThroughTheSDK(t *testing.T) {
	var opts ceremony.InitializeOptions = fullInitializeOptions()

	if opts.SOPin != "so-pin-1234" || opts.UserPin != "user-pin-5678" {
		t.Errorf("PINs did not survive the alias: SOPin=%q UserPin=%q", opts.SOPin, opts.UserPin)
	}
	if opts.CredentialSealStrategy != "passphrase" {
		t.Errorf("CredentialSealStrategy = %q, want passphrase", opts.CredentialSealStrategy)
	}
	if opts.Threshold != 2 || opts.Policy != "designated" {
		t.Errorf("Threshold=%d Policy=%q, want 2 and designated", opts.Threshold, opts.Policy)
	}
	if len(opts.Officers) != 3 || opts.Officers[0].Username != "alice" {
		t.Errorf("Officers = %+v, want alice, bob, carol", opts.Officers)
	}
}
