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

package kv

import "testing"

func TestKeyValueCloneDeepCopy(t *testing.T) {
	original := &KeyValue{
		Key:            []byte("test-key"),
		Value:          []byte("test-value"),
		CreateRevision: 1,
		ModRevision:    5,
		Version:        3,
		LeaseID:        42,
	}

	clone := original.Clone()

	if clone == original {
		t.Fatal("Clone() should return a different pointer")
	}
	if string(clone.Key) != string(original.Key) {
		t.Errorf("Key = %q, want %q", clone.Key, original.Key)
	}
	if string(clone.Value) != string(original.Value) {
		t.Errorf("Value = %q, want %q", clone.Value, original.Value)
	}
	if clone.CreateRevision != original.CreateRevision {
		t.Errorf("CreateRevision = %d, want %d", clone.CreateRevision, original.CreateRevision)
	}
	if clone.ModRevision != original.ModRevision {
		t.Errorf("ModRevision = %d, want %d", clone.ModRevision, original.ModRevision)
	}
	if clone.Version != original.Version {
		t.Errorf("Version = %d, want %d", clone.Version, original.Version)
	}
	if clone.LeaseID != original.LeaseID {
		t.Errorf("LeaseID = %d, want %d", clone.LeaseID, original.LeaseID)
	}

	// Mutate original to verify deep copy.
	original.Key[0] = 'X'
	original.Value[0] = 'X'
	if clone.Key[0] == 'X' {
		t.Error("Clone Key was mutated when original was modified")
	}
	if clone.Value[0] == 'X' {
		t.Error("Clone Value was mutated when original was modified")
	}
}

func TestKeyValueCloneNil(t *testing.T) {
	var kv *KeyValue
	clone := kv.Clone()
	if clone != nil {
		t.Errorf("Clone() of nil = %v, want nil", clone)
	}
}

func TestKeyValueCloneEmptySlices(t *testing.T) {
	original := &KeyValue{
		CreateRevision: 10,
		ModRevision:    20,
		Version:        1,
	}

	clone := original.Clone()

	if clone.Key != nil {
		t.Errorf("Key = %v, want nil", clone.Key)
	}
	if clone.Value != nil {
		t.Errorf("Value = %v, want nil", clone.Value)
	}
	if clone.CreateRevision != 10 {
		t.Errorf("CreateRevision = %d, want 10", clone.CreateRevision)
	}
}
