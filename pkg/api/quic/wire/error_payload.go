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
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
)

// ErrorPayload is the body of every response frame whose status is not
// StatusOK.
//
// On the wire it is a protobuf google.rpc.Status: Message is its message, and
// a delay is a google.rpc.RetryInfo detail -- byte for byte what a gRPC or Unix
// socket caller receives for the same refusal. The Status's code is the gRPC
// equivalent of the frame's status, so the body is a well-formed
// google.rpc.Status to any reader; the frame's status remains authoritative and
// a client classifies on it alone.
type ErrorPayload struct {
	// Message is the server's account of the failure, for people and logs.
	Message string

	// RetryAfter is how long the caller must wait before a retry can succeed,
	// meaningful only when HasRetryAfter is set: a throttled request, an SO
	// PIN lockout. It is REST's Retry-After and gRPC's RetryInfo on this
	// transport, at the same precision as RetryInfo.
	RetryAfter time.Duration

	// HasRetryAfter reports whether the failure names a delay.
	HasRetryAfter bool

	// Info is the failure's structured reason, a google.rpc.ErrorInfo detail
	// -- a replica refusal's replica set, for one -- or nil.
	Info *ErrorInfo
}

// ErrorInfo is a google.rpc.ErrorInfo: a reason within a domain, with
// metadata a reader acts on without parsing the message.
type ErrorInfo struct {
	Domain   string
	Reason   string
	Metadata map[string]string
}

// EncodeErrorPayload renders p as the body of an error frame whose status is
// status; the google.rpc.Status's code is that status's gRPC equivalent
// (GRPCCodeForStatus), or codes.Unknown for a status this build does not
// declare. A message that is not valid UTF-8 -- a key's bytes quoted in an
// error, say -- has each invalid sequence replaced by U+FFFD, because a
// protobuf string must be valid UTF-8 and the frame must still be written.
func EncodeErrorPayload(status uint16, p ErrorPayload) ([]byte, error) {
	code, declared := GRPCCodeForStatus(status)
	if !declared {
		code = codes.Unknown
	}
	st := &spb.Status{
		Code:    int32(code), //nolint:gosec // codes.Code is a uint32 enum whose values (0-16) fit an int32.
		Message: strings.ToValidUTF8(p.Message, "\uFFFD"),
	}
	var details []proto.Message
	if p.HasRetryAfter {
		details = append(details, &errdetails.RetryInfo{RetryDelay: durationpb.New(p.RetryAfter)})
	}
	if p.Info != nil {
		details = append(details, &errdetails.ErrorInfo{Domain: p.Info.Domain, Reason: p.Info.Reason, Metadata: p.Info.Metadata})
	}
	for _, d := range details {
		detail, err := anypb.New(d)
		if err != nil {
			return nil, &PayloadEncodeError{Err: err}
		}
		st.Details = append(st.Details, detail)
	}
	b, err := proto.Marshal(st)
	if err != nil {
		return nil, &PayloadEncodeError{Err: err}
	}
	return b, nil
}

// DecodeErrorPayload reads the body of an error frame. A body that is not a
// google.rpc.Status, or whose RetryInfo does not decode or names an invalid
// delay, is *PayloadDecodeError. Details other than RetryInfo are
// ignored, so a newer server may add some without breaking this reader.
func DecodeErrorPayload(b []byte) (ErrorPayload, error) {
	var st spb.Status
	if err := proto.Unmarshal(b, &st); err != nil {
		return ErrorPayload{}, &PayloadDecodeError{Err: err}
	}
	p := ErrorPayload{Message: st.GetMessage()}
	for _, detail := range st.GetDetails() {
		switch {
		case detail.MessageIs((*errdetails.RetryInfo)(nil)) && !p.HasRetryAfter:
			var ri errdetails.RetryInfo
			if err := detail.UnmarshalTo(&ri); err != nil {
				return ErrorPayload{}, &PayloadDecodeError{Err: err}
			}
			if err := ri.GetRetryDelay().CheckValid(); err != nil {
				return ErrorPayload{}, &PayloadDecodeError{Err: err}
			}
			p.RetryAfter, p.HasRetryAfter = ri.GetRetryDelay().AsDuration(), true
		case detail.MessageIs((*errdetails.ErrorInfo)(nil)) && p.Info == nil:
			var ei errdetails.ErrorInfo
			if err := detail.UnmarshalTo(&ei); err != nil {
				return ErrorPayload{}, &PayloadDecodeError{Err: err}
			}
			p.Info = &ErrorInfo{Domain: ei.GetDomain(), Reason: ei.GetReason(), Metadata: ei.GetMetadata()}
		}
	}
	return p, nil
}
