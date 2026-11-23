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

package engine

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/vector"
)

func TestCheckpointBody_RoundTrips(t *testing.T) {
	params := map[string]string{"m": "16", "ef": "200"}
	body, err := encodeCheckpointBody(42, vector.IndexHNSW, params, 7, []byte("payload-bytes"))
	if err != nil {
		t.Fatalf("encodeCheckpointBody: %v", err)
	}
	hdr, err := decodeCheckpointBody(body)
	if err != nil {
		t.Fatalf("decodeCheckpointBody: %v", err)
	}
	if hdr.version != checkpointFormatVersion {
		t.Fatalf("version = %d, want %d", hdr.version, checkpointFormatVersion)
	}
	if hdr.seq != 42 {
		t.Fatalf("seq = %d, want 42", hdr.seq)
	}
	if hdr.indexType != vector.IndexHNSW {
		t.Fatalf("indexType = %d, want %d", hdr.indexType, vector.IndexHNSW)
	}
	if hdr.generation != 7 {
		t.Fatalf("generation = %d, want 7", hdr.generation)
	}
	if hdr.params["m"] != "16" || hdr.params["ef"] != "200" {
		t.Fatalf("params = %v, want %v", hdr.params, params)
	}
	if string(hdr.payload) != "payload-bytes" {
		t.Fatalf("payload = %q, want %q", hdr.payload, "payload-bytes")
	}
}

func TestDecodeCheckpointBody_TooShortIsRefused(t *testing.T) {
	if _, err := decodeCheckpointBody(make([]byte, 5)); err == nil {
		t.Fatal("decodeCheckpointBody(too short) = nil error, want one")
	}
}

func TestDecodeCheckpointBody_TruncatedParamBlockIsRefused(t *testing.T) {
	// A valid 18-byte header with no param block bytes following it: long
	// enough to pass the length floor, too short for ReadParamBlock.
	data := make([]byte, 18)
	if _, err := decodeCheckpointBody(data); err == nil {
		t.Fatal("decodeCheckpointBody(truncated param block) = nil error, want one")
	}
}
