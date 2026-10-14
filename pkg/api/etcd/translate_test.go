//go:build qrdb_etcd

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

package etcd

import (
	"bytes"
	"testing"

	kv "github.com/jeremyhahn/go-qrdb/pkg/kv"
)

func TestKvToEtcd_NilReturnsNil(t *testing.T) {
	result := kvToEtcd(nil)
	if result != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestKvToEtcd_MapsAllFields(t *testing.T) {
	src := &kv.KeyValue{
		Key:            []byte("hello"),
		Value:          []byte("world"),
		CreateRevision: 10,
		ModRevision:    20,
		Version:        3,
		LeaseID:        42,
	}
	got := kvToEtcd(src)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if !bytes.Equal(got.Key, src.Key) {
		t.Fatalf("Key mismatch: got %q, want %q", got.Key, src.Key)
	}
	if !bytes.Equal(got.Value, src.Value) {
		t.Fatalf("Value mismatch: got %q, want %q", got.Value, src.Value)
	}
	if got.CreateRevision != src.CreateRevision {
		t.Fatalf("CreateRevision mismatch: got %d, want %d", got.CreateRevision, src.CreateRevision)
	}
	if got.ModRevision != src.ModRevision {
		t.Fatalf("ModRevision mismatch: got %d, want %d", got.ModRevision, src.ModRevision)
	}
	if got.Version != src.Version {
		t.Fatalf("Version mismatch: got %d, want %d", got.Version, src.Version)
	}
	if got.Lease != src.LeaseID {
		t.Fatalf("Lease mismatch: got %d, want %d", got.Lease, src.LeaseID)
	}
}

func TestKvToEtcd_EmptyKeyAndValue(t *testing.T) {
	src := &kv.KeyValue{
		Key:   []byte{},
		Value: []byte{},
	}
	got := kvToEtcd(src)
	if got == nil {
		t.Fatal("expected non-nil result for empty key/value")
	}
	if len(got.Key) != 0 {
		t.Fatalf("expected empty Key, got %q", got.Key)
	}
	if len(got.Value) != 0 {
		t.Fatalf("expected empty Value, got %q", got.Value)
	}
}

func TestKvsToEtcd_NilReturnsNil(t *testing.T) {
	result := kvsToEtcd(nil)
	if result != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestKvsToEtcd_EmptySliceReturnsNil(t *testing.T) {
	result := kvsToEtcd([]*kv.KeyValue{})
	if result != nil {
		t.Fatal("expected nil for empty slice")
	}
}

func TestKvsToEtcd_MultipleElements(t *testing.T) {
	input := []*kv.KeyValue{
		{Key: []byte("a"), Value: []byte("1"), CreateRevision: 1, ModRevision: 2, Version: 1, LeaseID: 0},
		{Key: []byte("b"), Value: []byte("2"), CreateRevision: 3, ModRevision: 4, Version: 2, LeaseID: 99},
	}
	got := kvsToEtcd(input)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(got))
	}
	if !bytes.Equal(got[0].Key, []byte("a")) {
		t.Fatalf("first Key mismatch: got %q", got[0].Key)
	}
	if !bytes.Equal(got[1].Key, []byte("b")) {
		t.Fatalf("second Key mismatch: got %q", got[1].Key)
	}
	if got[1].Lease != 99 {
		t.Fatalf("second Lease mismatch: got %d, want 99", got[1].Lease)
	}
}

func TestKvsToEtcd_ContainsNilElement(t *testing.T) {
	input := []*kv.KeyValue{
		{Key: []byte("x"), Value: []byte("y")},
		nil,
	}
	got := kvsToEtcd(input)
	if len(got) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(got))
	}
	if got[0] == nil {
		t.Fatal("expected first element to be non-nil")
	}
	if got[1] != nil {
		t.Fatal("expected second element to be nil")
	}
}

func TestResponseHeader_SetsAllFields(t *testing.T) {
	hdr := responseHeader(100, 200, 300, 400)
	if hdr.ClusterId != 100 {
		t.Fatalf("ClusterId mismatch: got %d, want 100", hdr.ClusterId)
	}
	if hdr.MemberId != 200 {
		t.Fatalf("MemberId mismatch: got %d, want 200", hdr.MemberId)
	}
	if hdr.Revision != 300 {
		t.Fatalf("Revision mismatch: got %d, want 300", hdr.Revision)
	}
	if hdr.RaftTerm != 400 {
		t.Fatalf("RaftTerm mismatch: got %d, want 400", hdr.RaftTerm)
	}
}

func TestResponseHeader_ZeroValues(t *testing.T) {
	hdr := responseHeader(0, 0, 0, 0)
	if hdr.ClusterId != 0 || hdr.MemberId != 0 || hdr.Revision != 0 || hdr.RaftTerm != 0 {
		t.Fatal("expected all zero values in header")
	}
}

func TestIsAllKeys(t *testing.T) {
	tests := []struct {
		name     string
		key      []byte
		rangeEnd []byte
		want     bool
	}{
		{
			name:     "true_both_zero_byte",
			key:      []byte{0},
			rangeEnd: []byte{0},
			want:     true,
		},
		{
			name:     "false_key_wrong_length",
			key:      []byte{0, 0},
			rangeEnd: []byte{0},
			want:     false,
		},
		{
			name:     "false_rangeEnd_wrong_length",
			key:      []byte{0},
			rangeEnd: []byte{0, 0},
			want:     false,
		},
		{
			name:     "false_key_nonzero",
			key:      []byte{1},
			rangeEnd: []byte{0},
			want:     false,
		},
		{
			name:     "false_rangeEnd_nonzero",
			key:      []byte{0},
			rangeEnd: []byte{1},
			want:     false,
		},
		{
			name:     "false_both_empty",
			key:      []byte{},
			rangeEnd: []byte{},
			want:     false,
		},
		{
			name:     "false_nil_inputs",
			key:      nil,
			rangeEnd: nil,
			want:     false,
		},
		{
			name:     "false_both_nonzero",
			key:      []byte{1},
			rangeEnd: []byte{1},
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAllKeys(tt.key, tt.rangeEnd)
			if got != tt.want {
				t.Fatalf("isAllKeys(%v, %v) = %v, want %v", tt.key, tt.rangeEnd, got, tt.want)
			}
		})
	}
}
