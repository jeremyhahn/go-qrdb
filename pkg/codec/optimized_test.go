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

//go:build codec_msgpack

package codec

import "testing"

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkJSONCodec_Marshal(b *testing.B) {
	c := &JSONCodec{}
	payload := samplePayload()

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = c.Marshal(payload)
	}
}

func BenchmarkMsgPackCodec_Marshal(b *testing.B) {
	c := &MsgPackCodec{}
	payload := samplePayload()

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = c.Marshal(payload)
	}
}

func BenchmarkOptimizedJSON_Marshal(b *testing.B) {
	oc := NewOptimizedCodec(&JSONCodec{})
	payload := samplePayload()

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = oc.Marshal(payload)
	}
}

func BenchmarkOptimizedMsgPack_Marshal(b *testing.B) {
	oc := NewOptimizedCodec(&MsgPackCodec{})
	payload := samplePayload()

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = oc.Marshal(payload)
	}
}

func BenchmarkJSONCodec_Unmarshal(b *testing.B) {
	c := &JSONCodec{}
	payload := samplePayload()
	data, _ := c.Marshal(payload)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		var out testPayload
		_ = c.Unmarshal(data, &out)
	}
}

func BenchmarkMsgPackCodec_Unmarshal(b *testing.B) {
	c := &MsgPackCodec{}
	payload := samplePayload()
	data, _ := c.Marshal(payload)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		var out testPayload
		_ = c.Unmarshal(data, &out)
	}
}

func BenchmarkOptimizedMsgPack_MarshalTo(b *testing.B) {
	oc := NewOptimizedCodec(&MsgPackCodec{})
	payload := samplePayload()
	buf := make([]byte, 0, 1024)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		buf, _ = oc.MarshalTo(payload, buf[:0])
	}
}

func BenchmarkManager_Transcode(b *testing.B) {
	m := NewManager(
		NewOptimizedCodec(&MsgPackCodec{}),
		NewOptimizedCodec(&JSONCodec{}),
	)
	payload := samplePayload()
	data, _ := m.EncodeForStorage(payload)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		var intermediate testPayload
		_, _ = m.Transcode(data, &intermediate)
	}
}
