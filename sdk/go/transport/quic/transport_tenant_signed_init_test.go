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

package quic

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestTransport_TenantBarrierSignedInitBegin_Closed(t *testing.T) {
	tr := newClosedTenantTransport(t)
	if _, err := tr.TenantBarrierSignedInitBegin(context.Background(), &transport.TenantBarrierSignedInitBeginRequest{TenantID: "acme"}); !errors.As(err, new(*transport.ClosedError)) {
		t.Fatalf("err = %v, want *transport.ClosedError", err)
	}
}

func TestTransport_TenantBarrierSignedInitBegin_NilRequest(t *testing.T) {
	tr := newClosedTenantTransport(t)
	if _, err := tr.TenantBarrierSignedInitBegin(context.Background(), nil); err == nil {
		t.Fatal("a nil request returned no error")
	}
}

func TestTransport_TenantBarrierSignedInitComplete_Closed(t *testing.T) {
	tr := newClosedTenantTransport(t)
	if _, err := tr.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{TenantID: "acme"}); !errors.As(err, new(*transport.ClosedError)) {
		t.Fatalf("err = %v, want *transport.ClosedError", err)
	}
}

func TestTransport_TenantBarrierSignedInitComplete_NilRequest(t *testing.T) {
	tr := newClosedTenantTransport(t)
	if _, err := tr.TenantBarrierSignedInitComplete(context.Background(), nil); err == nil {
		t.Fatal("a nil request returned no error")
	}
}
