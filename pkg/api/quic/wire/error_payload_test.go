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

package wire

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
)

// TestErrorPayload_RoundTrips: the message and the delay survive the frame,
// a delay of zero is still a delay, and no delay stays none.
func TestErrorPayload_RoundTrips(t *testing.T) {
	for name, p := range map[string]ErrorPayload{
		"message only":      {Message: "key not found"},
		"with a delay":      {Message: "SO PIN locked", RetryAfter: 1500 * time.Millisecond, HasRetryAfter: true},
		"retry now":         {Message: "throttled", HasRetryAfter: true},
		"no message at all": {},
	} {
		body, err := EncodeErrorPayload(StatusResourceExhausted, p)
		if err != nil {
			t.Fatalf("%s: encode: %v", name, err)
		}
		got, err := DecodeErrorPayload(body)
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if got != p {
			t.Errorf("%s: round trip = %+v, want %+v", name, got, p)
		}
	}
}

// TestEncodeErrorPayload_IsAGoogleRPCStatus: the body is the google.rpc.Status
// a gRPC caller would receive for the same refusal -- the status's gRPC code,
// the message, and the delay as a RetryInfo detail -- so any reader that knows
// google.rpc.Status reads it correctly.
func TestEncodeErrorPayload_IsAGoogleRPCStatus(t *testing.T) {
	body, err := EncodeErrorPayload(StatusResourceExhausted, ErrorPayload{Message: "locked", RetryAfter: 4 * time.Second, HasRetryAfter: true})
	if err != nil {
		t.Fatal(err)
	}
	var st spb.Status
	if err := proto.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if codes.Code(st.GetCode()) != codes.ResourceExhausted || st.GetMessage() != "locked" || len(st.GetDetails()) != 1 {
		t.Fatalf("status = %v", &st)
	}
	var ri errdetails.RetryInfo
	if err := st.GetDetails()[0].UnmarshalTo(&ri); err != nil || ri.GetRetryDelay().AsDuration() != 4*time.Second {
		t.Fatalf("RetryInfo = %v, %v", &ri, err)
	}

	unknown, err := EncodeErrorPayload(StatusUnknown+1, ErrorPayload{Message: "newer"})
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(unknown, &st); err != nil || codes.Code(st.GetCode()) != codes.Unknown {
		t.Fatalf("an undeclared status's code = %v, %v; want Unknown", codes.Code(st.GetCode()), err)
	}
}

// TestEncodeErrorPayload_RepairsInvalidUTF8: a message carrying bytes that are
// not UTF-8 still makes a frame, with the bad bytes replaced.
func TestEncodeErrorPayload_RepairsInvalidUTF8(t *testing.T) {
	body, err := EncodeErrorPayload(StatusNotFound, ErrorPayload{Message: "key \xff\xfe missing"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeErrorPayload(body)
	if err != nil || got.Message != "key \uFFFD missing" {
		t.Fatalf("decoded %q, %v", got.Message, err)
	}
}

// TestDecodeErrorPayload_RefusesMalformedBodies: bare text (a qrdb-quic/2
// body), a RetryInfo that does not decode, and an invalid delay are each an
// *PayloadDecodeError, never a message.
func TestDecodeErrorPayload_RefusesMalformedBodies(t *testing.T) {
	brokenDetail, err := proto.Marshal(&spb.Status{Message: "m", Details: []*anypb.Any{{
		TypeUrl: "type.googleapis.com/google.rpc.RetryInfo", Value: []byte{0xff},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	invalidDelay, err := anypb.New(&errdetails.RetryInfo{RetryDelay: &durationpb.Duration{Seconds: 1, Nanos: -1}})
	if err != nil {
		t.Fatal(err)
	}
	badDelay, err := proto.Marshal(&spb.Status{Message: "m", Details: []*anypb.Any{invalidDelay}})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"bare text":                 []byte("\xffkey not found"),
		"undecodable RetryInfo":     brokenDetail,
		"a delay that is not valid": badDelay,
	} {
		_, err := DecodeErrorPayload(body)
		var decodeErr *PayloadDecodeError
		if !errors.As(err, &decodeErr) || decodeErr.Unwrap() == nil || decodeErr.Error() == "" {
			t.Errorf("%s: err = %v, want *PayloadDecodeError", name, err)
		}
	}
}

// TestDecodeErrorPayload_IgnoresOtherDetails: a detail this build does not
// read is skipped, and the RetryInfo after it is still found.
func TestDecodeErrorPayload_IgnoresOtherDetails(t *testing.T) {
	other, err := anypb.New(&errdetails.ErrorInfo{Reason: "LOCKED"})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := anypb.New(&errdetails.RetryInfo{RetryDelay: durationpb.New(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	body, err := proto.Marshal(&spb.Status{Message: "m", Details: []*anypb.Any{other, retry}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeErrorPayload(body)
	if err != nil || !got.HasRetryAfter || got.RetryAfter != time.Second {
		t.Fatalf("decoded %+v, %v", got, err)
	}
}

// TestPayloadEncodeError_Unwraps: the encode error names its cause.
func TestPayloadEncodeError_Unwraps(t *testing.T) {
	cause := errors.New("cause")
	err := &PayloadEncodeError{Err: cause}
	if !errors.Is(err, cause) || err.Error() == "" {
		t.Fatalf("err = %v", err)
	}
}
