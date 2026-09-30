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

package sdk

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestWithSOPIN_IsTheTransportBinding(t *testing.T) {
	var _ *SOPINConflictError = (*transport.SOPINConflictError)(nil)
	ctx, release := WithSOPIN(context.Background(), []byte("1234"))
	if pin, ok := transport.SOPINFromContext(ctx); !ok || string(pin) != "1234" {
		t.Fatalf("the transports do not see the PIN sdk.WithSOPIN bound: %q, %v", pin, ok)
	}
	release()
	if _, ok := transport.SOPINFromContext(ctx); ok {
		t.Fatal("the PIN survived release")
	}
}
