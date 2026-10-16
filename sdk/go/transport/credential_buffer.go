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

package transport

import (
	"encoding/base64"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// BufferFromWire moves a credential value that a remote transport decoded from
// its secret-bearing wire DTO into protected memory and wipes the decoded
// slice, so the plain copy lives only as long as the call that decoded it.
//
// A remote transport cannot avoid the plaintext in its response body and
// decoder buffers, which this package does not own and cannot wipe; the DTO is
// the single boundary where the value is in plain memory, and this is the first
// point after it that the SDK owns the bytes. The returned buffer is owned by
// the caller, who MUST Destroy it on every path.
//
// A nil or empty value returns *secure.InvalidSizeError.
func BufferFromWire(value []byte) (*secure.Buffer, error) {
	return secure.NewBufferFromBytes(value) // wipes value
}

// BufferFromBase64 decodes standard padded base64 text straight into a protected
// buffer: the decoded credential is written into the buffer's window and never
// exists as a heap slice. src is wiped on every path, so the encoded copy the
// caller handed over does not outlive the call either.
//
// Input that is not well-formed padded base64 returns base64.CorruptInputError;
// input that decodes to nothing returns *secure.InvalidSizeError. The buffer is
// owned by the caller, who MUST Destroy it, and is nil on every error.
func BufferFromBase64(src []byte) (*secure.Buffer, error) {
	defer secure.Wipe(src)
	if len(src)%4 != 0 {
		return nil, base64.CorruptInputError(len(src))
	}
	size := len(src) / 4 * 3
	for pad := 0; pad < 2 && size > 0 && src[len(src)-1-pad] == '='; pad++ {
		size--
	}
	buf, err := secure.NewBuffer(size)
	if err != nil {
		return nil, err
	}
	if err := buf.WithBytes(func(dst []byte) error {
		_, decodeErr := base64.StdEncoding.Decode(dst, src)
		return decodeErr
	}); err != nil {
		buf.Destroy()
		return nil, err
	}
	return buf, nil
}

// WireValue is a JSON string field holding a base64 credential value that
// decodes directly into protected memory instead of a []byte. Use it as the
// field type in a response struct decoded by encoding/json; Buffer is nil until
// a value is decoded and, once set, is owned by the caller, who MUST Destroy it.
//
// encoding/json hands UnmarshalJSON a slice of its own read buffer, which is
// wiped here once decoded; the decoder's other internal copies are the
// framework's and not reachable from this package.
type WireValue struct {
	Buffer *secure.Buffer
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *WireValue) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		secure.Wipe(data)
		return &WireValueError{}
	}
	buf, err := BufferFromBase64(data[1 : len(data)-1])
	secure.Wipe(data)
	if err != nil {
		return err
	}
	v.Buffer.Destroy()
	v.Buffer = buf
	return nil
}

// WireValueError reports a credential value field that is not a JSON string.
type WireValueError struct{}

// Error implements error.
func (*WireValueError) Error() string {
	return "transport: credential value is not a JSON string"
}
