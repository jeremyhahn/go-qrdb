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

package quic

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// captureIndexCreateRequest drives the QUIC client's IndexCreate with config
// and returns the protobuf message it actually put on the wire.
func captureIndexCreateRequest(t *testing.T, config transport.IndexConfig) *pb.IndexCreateRequest {
	t.Helper()
	srv, tr := startE2EServer(t)

	got := &pb.IndexCreateRequest{}
	srv.SetResponder("index.Create", func(payload []byte) (uint16, []byte) {
		if err := proto.Unmarshal(payload, got); err != nil {
			t.Errorf("unmarshal IndexCreateRequest: %v", err)
		}
		return wire.StatusOK, nil
	})

	if err := tr.IndexCreate(context.Background(), "by_email", "email", config); err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}
	return got
}

// TestE2E_IndexCreate_EveryConfigFieldReachesTheWire covers go-qrdb#34 on the
// QUIC client. The marshalled request carried Unique and Sparse only: Numeric
// was a transport.IndexConfig field no request ever serialised, and
// case-insensitivity could not be expressed at all. A config field the client
// silently drops makes the server build a different index while the caller is
// told it succeeded.
func TestE2E_IndexCreate_EveryConfigFieldReachesTheWire(t *testing.T) {
	got := captureIndexCreateRequest(t, transport.IndexConfig{
		Unique:          true,
		Sparse:          true,
		Numeric:         true,
		CaseInsensitive: true,
	})

	if got.GetName() != "by_email" || got.GetFieldName() != "email" {
		t.Errorf("name/field = %q/%q; want by_email/email", got.GetName(), got.GetFieldName())
	}
	if !got.GetUnique() || !got.GetSparse() {
		t.Errorf("unique/sparse lost: %+v", got)
	}
	if !got.GetNumeric() {
		t.Error("Numeric never reached the wire")
	}
	if !got.GetCaseInsensitive() {
		t.Error("CaseInsensitive never reached the wire")
	}
}

// TestE2E_IndexCreate_ZeroConfigSendsNoClaims is the control and the polarity
// guard: every flag on this wire marks a POSITIVE claim, so a zero config must
// leave all of them unset — an ordinary index, case-SENSITIVE, string-ordered.
func TestE2E_IndexCreate_ZeroConfigSendsNoClaims(t *testing.T) {
	got := captureIndexCreateRequest(t, transport.IndexConfig{})

	if got.GetUnique() || got.GetSparse() || got.GetNumeric() || got.GetCaseInsensitive() {
		t.Errorf("a zero config sent %+v; want no claims set", got)
	}
}
