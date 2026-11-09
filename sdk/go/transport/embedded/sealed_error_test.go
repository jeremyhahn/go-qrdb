// Copyright (c) 2026 Jeremy Hahn
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

package embedded

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The embedded refusal matches transport.IsBarrierSealed like every network
// transport's, and still matches seal.ErrSealed.
func TestBarrierSealedError_MatchesTheTransportSealedError(t *testing.T) {
	for name, err := range map[string]error{
		"bare":    &BarrierSealedError{Err: seal.ErrSealed},
		"wrapped": fmt.Errorf("call: %w", &BarrierSealedError{}),
	} {
		if !transport.IsBarrierSealed(err) {
			t.Errorf("%s: transport.IsBarrierSealed = false", name)
		}
		if !errors.Is(err, seal.ErrSealed) {
			t.Errorf("%s: errors.Is(seal.ErrSealed) = false", name)
		}
		var sealed *transport.BarrierSealedError
		if errors.As(err, &sealed) && sealed.Message == "" {
			t.Errorf("%s: transport error carries no message", name)
		}
	}
}

func TestBarrierSealedError_AsRefusesOtherTargets(t *testing.T) {
	var reqErr *transport.RequestError
	if errors.As(&BarrierSealedError{}, &reqErr) {
		t.Fatal("matched an unrelated target")
	}
}

// A BarrierSealedError names and unwraps the cause it carries, and still
// reads as a message when it carries none.
func TestBarrierSealedError_CarriesItsCause(t *testing.T) {
	if (&BarrierSealedError{}).Error() == "" {
		t.Fatal("Error() is empty without a cause")
	}
	inner := errors.New("inner")
	e := &BarrierSealedError{Err: inner}
	if !strings.Contains(e.Error(), "inner") {
		t.Fatalf("Error() = %q, want it to name the cause", e.Error())
	}
	if !errors.Is(e, inner) {
		t.Fatal("errors.Is(e, cause) = false")
	}
}
