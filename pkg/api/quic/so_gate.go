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
	"unicode/utf8"

	quicgo "github.com/quic-go/quic-go"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// soPINField is the request-message field that carries the SO PIN on QUIC.
const soPINField = "so_pin"

// soPINCarriers names, for every SO-gated QUIC method, the descriptor of its
// request message, whose so_pin field carries the SO PIN.
//
// A QUIC frame has no header section, so unlike REST (the X-QRDB-SO-PIN
// header), gRPC and Unix (x-qrdb-so-pin metadata) and MCP (the header on the
// /mcp request), the only per-request carrier QUIC has is the request message
// itself. barrier.RecoveryDelete's request used to be empty and so had no
// carrier; it is now BarrierRecoveryDeleteRequest.
//
// A gated method absent from this table would present no PIN and the gate
// would refuse it (fail-closed), never admit it.
// TestSOGate_EveryGatedQUICMethodHasACarrier pins the table against the
// method registry, so a newly gated method cannot silently lack one.
var soPINCarriers = map[string]protoreflect.MessageDescriptor{
	"barrier.ShamirInitialize": (&pb.BarrierShamirInitializeRequest{}).ProtoReflect().Descriptor(),
	"barrier.RekeyStart":       (&pb.BarrierRekeyStartRequest{}).ProtoReflect().Descriptor(),
	"barrier.RecoveryGenerate": (&pb.BarrierRecoveryGenerateRequest{}).ProtoReflect().Descriptor(),
	"barrier.RecoveryDelete":   (&pb.BarrierRecoveryDeleteRequest{}).ProtoReflect().Descriptor(),
	"barrier.ChangePassphrase": (&pb.BarrierChangePassphraseRequest{}).ProtoReflect().Descriptor(),
	"ceremony.Initialize":      (&pb.CeremonyInitializeRequest{}).ProtoReflect().Descriptor(),
	"ceremony.SignCSR":         (&pb.CeremonySignCSRRequest{}).ProtoReflect().Descriptor(),
}

// soPINFromPayload returns a copy of the SO PIN a gated method's request
// carries, or nil when the method has no carrier, the payload is not a
// well-formed protobuf message, or so_pin is empty.
//
// It scans the wire format instead of decoding the message: a carrier such
// as BarrierChangePassphraseRequest also holds the current and next
// passphrases, and proto.Unmarshal would copy them into slices this gate
// never wipes. Only so_pin's bytes are copied; every other field is stepped
// over in place. The scan reads the payload as proto.Unmarshal does: the
// last so_pin occurrence wins, and a malformed field anywhere or a PIN that
// is not UTF-8 yields nil, so the gate fails closed.
func soPINFromPayload(method string, payload []byte) []byte {
	desc, ok := soPINCarriers[method]
	if !ok {
		return nil
	}
	fd := desc.Fields().ByName(soPINField)
	if fd == nil || fd.Kind() != protoreflect.StringKind {
		return nil
	}
	var pin []byte
	for b := payload; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil
		}
		b = b[n:]
		// A so_pin of another wire type is an unknown field to
		// proto.Unmarshal, and is skipped here as one.
		if num != fd.Number() || typ != protowire.BytesType {
			if n = protowire.ConsumeFieldValue(num, typ, b); n < 0 {
				return nil
			}
			b = b[n:]
			continue
		}
		v, m := protowire.ConsumeBytes(b)
		if m < 0 || !utf8.Valid(v) {
			return nil
		}
		b = b[m:]
		pin = v
	}
	if len(pin) == 0 {
		return nil
	}
	// A copy, not a subslice: the SO factor wipes what it is given, and the
	// handler decodes this payload after the gate.
	return append([]byte(nil), pin...)
}

// enforceSOGate applies the SO dual-factor gate to one request, after
// authorization. QUIC is TLS-only, so the factor always arrives over TLS.
func (s *Server) enforceSOGate(ctx context.Context, conn *quicgo.Conn, method string, payload []byte) error {
	perm, ok := s.methodPerms.Lookup(method)
	if !ok {
		return nil
	}
	if _, gated := authmw.SOPermissions[perm]; !gated {
		return nil
	}
	factor := authmw.NewSOFactor(ctx, soPINFromPayload(method, payload), true, conn.RemoteAddr().String())
	defer factor.Destroy()
	return s.soGate.Enforce(authmw.WithSOFactor(ctx, factor), perm, method, "call")
}
