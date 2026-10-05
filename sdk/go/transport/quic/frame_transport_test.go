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
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// sentFrame records the frame a FrameSender was handed.
type sentFrame struct {
	method, database string
	payload          []byte
}

// TestFrameTransport_EncodesTheRequestFrameAndDecodesTheAnswer proves a call
// on a frame transport hands over exactly the frame a connected transport
// writes -- method, database scope, payload -- and decodes the answer as one.
func TestFrameTransport_EncodesTheRequestFrameAndDecodesTheAnswer(t *testing.T) {
	var got sentFrame
	answer, err := proto.Marshal(&pb.GetResponse{Value: []byte("v1")})
	if err != nil {
		t.Fatal(err)
	}
	tr := NewFrameTransport("orders", func(_ context.Context, method, database string, payload []byte) (uint16, []byte, error) {
		got = sentFrame{method, database, payload}
		return wire.StatusOK, answer, nil
	})
	v, err := tr.Get(context.Background(), "k1")
	if err != nil || string(v) != "v1" {
		t.Fatalf("Get = %q, %v; want v1", v, err)
	}
	var req pb.GetRequest
	if err := proto.Unmarshal(got.payload, &req); err != nil || req.GetKey() != "k1" {
		t.Fatalf("payload = %v (%v); want the GetRequest for k1", &req, err)
	}
	if got.method != "kv.Get" || got.database != "orders" {
		t.Fatalf("frame = %s on %q; want kv.Get on orders", got.method, got.database)
	}

	scoped, err := tr.WithDatabase("billing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.(*Transport).Get(context.Background(), "k1"); err != nil || got.database != "billing" {
		t.Fatalf("a re-scoped frame transport sent on %q (%v); want billing", got.database, err)
	}
}

// TestFrameTransport_MapsAnErrorStatus proves an error frame is mapped as a
// connected transport maps it.
func TestFrameTransport_MapsAnErrorStatus(t *testing.T) {
	body, err := wire.EncodeErrorPayload(wire.StatusPermissionDenied, wire.ErrorPayload{Message: "kv:write denied"})
	if err != nil {
		t.Fatal(err)
	}
	tr := NewFrameTransport("orders", func(context.Context, string, string, []byte) (uint16, []byte, error) {
		return wire.StatusPermissionDenied, body, nil
	})
	err = tr.Put(context.Background(), "k1", []byte("v"))
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != int(wire.StatusPermissionDenied) {
		t.Fatalf("Put = %v; want a RequestError carrying the permission-denied status", err)
	}
}

// failure is a FrameSender's own error.
type failure struct{}

func (failure) Error() string { return "no answer" }

// TestFrameTransport_ASenderErrorIsTheCallsError proves a frame that drew no
// answer fails the call with the sender's error inside.
func TestFrameTransport_ASenderErrorIsTheCallsError(t *testing.T) {
	tr := NewFrameTransport("orders", func(context.Context, string, string, []byte) (uint16, []byte, error) {
		return 0, nil, failure{}
	})
	err := tr.Put(context.Background(), "k1", []byte("v"))
	var f failure
	if !errors.As(err, &f) {
		t.Fatalf("Put = %v; want the sender's error", err)
	}
}

// TestFrameTransport_RefusesAStream proves a watch, which has no single
// frame, is refused rather than opened on a connection the transport lacks.
func TestFrameTransport_RefusesAStream(t *testing.T) {
	sent := false
	tr := NewFrameTransport("orders", func(context.Context, string, string, []byte) (uint16, []byte, error) {
		sent = true
		return wire.StatusOK, nil, nil
	})
	ch, err := tr.Watch(context.Background(), "k1", transport.WatchOptions{})
	var unsupported *transport.CapabilityUnsupportedError
	if ch != nil || !errors.As(err, &unsupported) || sent {
		t.Fatalf("Watch = %v, %v (sent %v); want *transport.CapabilityUnsupportedError and nothing sent", ch, err, sent)
	}
}
