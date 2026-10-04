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
	"encoding/binary"
	"math"
)

// MethodOnBehalf is the method of a request a cluster node forwards on a
// caller's behalf (docs/replica-forwarding). Its payload is an OnBehalf
// envelope: the caller's method and payload, and the signed assertion that
// the caller made them. The request frame itself is unchanged -- it has no
// header to carry an assertion in -- so a forward is a method of its own,
// served only to a cluster node whose certificate the assertion verifies
// against, and answered as the inner method would be.
const MethodOnBehalf = "forward.OnBehalf"

// OnBehalf is a forwarded request: the caller's method and payload, and the
// on-behalf assertion and its signature, both base64 as forward.Sign makes
// them.
type OnBehalf struct {
	Method    string
	Assertion string
	Signature string
	Payload   []byte
}

// EncodeOnBehalf encodes o as a MethodOnBehalf payload:
//
//	[methodLen:uint16][method][assertionLen:uint16][assertion][sigLen:uint16][sig][payload]
func EncodeOnBehalf(o OnBehalf) ([]byte, error) {
	if len(o.Method) > MaxMethodLen {
		return nil, &MethodTooLongError{Length: len(o.Method), Max: MaxMethodLen}
	}
	for field, v := range map[string]string{"assertion": o.Assertion, "signature": o.Signature} {
		if len(v) > math.MaxUint16 {
			return nil, &OnBehalfFieldError{Field: field}
		}
	}
	b := make([]byte, 0, 6+len(o.Method)+len(o.Assertion)+len(o.Signature)+len(o.Payload))
	for _, field := range []string{o.Method, o.Assertion, o.Signature} {
		b = binary.BigEndian.AppendUint16(b, uint16(len(field))) //nolint:gosec // bounded above
		b = append(b, field...)
	}
	return append(b, o.Payload...), nil
}

// DecodeOnBehalf decodes a MethodOnBehalf payload. A truncated envelope,
// an empty method or an over-long one is *OnBehalfFieldError.
func DecodeOnBehalf(b []byte) (OnBehalf, error) {
	var fields [3]string
	for i, name := range []string{"method", "assertion", "signature"} {
		if len(b) < 2 {
			return OnBehalf{}, &OnBehalfFieldError{Field: name}
		}
		n := int(binary.BigEndian.Uint16(b))
		b = b[2:]
		if len(b) < n {
			return OnBehalf{}, &OnBehalfFieldError{Field: name}
		}
		fields[i], b = string(b[:n]), b[n:]
	}
	if fields[0] == "" || len(fields[0]) > MaxMethodLen {
		return OnBehalf{}, &OnBehalfFieldError{Field: "method"}
	}
	return OnBehalf{Method: fields[0], Assertion: fields[1], Signature: fields[2], Payload: b}, nil
}

// OnBehalfFieldError reports a MethodOnBehalf envelope field that does not
// encode or decode: truncated, empty where it may not be, or too long.
type OnBehalfFieldError struct {
	Field string
}

func (e *OnBehalfFieldError) Error() string {
	return "wire: on-behalf envelope field " + e.Field + " is malformed"
}
