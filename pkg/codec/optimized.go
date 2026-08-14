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

package codec

import "sync"

const defaultBufSize = 512

// OptimizedCodec wraps any Codec with sync.Pool buffer reuse to reduce
// allocations on hot paths. It implements the Codec interface and adds
// a MarshalTo method for pre-allocated buffer marshaling.
type OptimizedCodec struct {
	inner Codec
	pool  sync.Pool
}

// NewOptimizedCodec creates an OptimizedCodec wrapping the given codec.
// Buffers are pooled and reused across Marshal calls.
func NewOptimizedCodec(inner Codec) *OptimizedCodec {
	return &OptimizedCodec{
		inner: inner,
		pool: sync.Pool{
			New: func() any {
				buf := make([]byte, 0, defaultBufSize)
				return &buf
			},
		},
	}
}

// Marshal encodes v using the inner codec, leveraging pooled buffers to
// reduce allocation pressure. The returned slice is a copy that is safe
// to retain; the pooled buffer is returned to the pool after copying.
func (o *OptimizedCodec) Marshal(v any) ([]byte, error) {
	data, err := o.inner.Marshal(v)
	if err != nil {
		return nil, err
	}

	// Get a pooled buffer, copy the result, then return the pool buffer.
	bufPtr := o.pool.Get().(*[]byte) //nolint:errcheck // type assertion on a value this container only ever holds
	buf := *bufPtr

	// Ensure capacity.
	if cap(buf) < len(data) {
		buf = make([]byte, len(data))
	} else {
		buf = buf[:len(data)]
	}
	copy(buf, data)

	// Make a result copy so the caller owns the memory.
	result := make([]byte, len(buf))
	copy(result, buf)

	// Return buffer to pool.
	*bufPtr = buf[:0]
	o.pool.Put(bufPtr)

	return result, nil
}

// Unmarshal decodes data into v using the inner codec.
func (o *OptimizedCodec) Unmarshal(data []byte, v any) error {
	return o.inner.Unmarshal(data, v)
}

// Type returns the type string of the inner codec.
func (o *OptimizedCodec) Type() string {
	return o.inner.Type()
}

// MarshalTo encodes v into the pre-allocated buffer buf, growing it if
// necessary. It returns the buffer (potentially reallocated) containing
// the encoded data. The caller retains ownership of the returned slice.
func (o *OptimizedCodec) MarshalTo(v any, buf []byte) ([]byte, error) {
	data, err := o.inner.Marshal(v)
	if err != nil {
		return buf, err
	}

	if cap(buf) < len(data) {
		buf = make([]byte, len(data))
	} else {
		buf = buf[:len(data)]
	}
	copy(buf, data)
	return buf, nil
}

// Inner returns the wrapped codec. This is useful for type assertions
// or inspecting the underlying codec implementation.
func (o *OptimizedCodec) Inner() Codec {
	return o.inner
}
