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

package vlogwire

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

func encode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestKey_CarriesVersionAndPublicKeyNotTheKeyRef(t *testing.T) {
	got := Key(vlog.HeadKey{Version: 3, Ref: "software/v3", PublicKey: []byte{1, 2}})
	if s := encode(t, got); s != `{"version":3,"public_key":"AQI="}` {
		t.Fatalf("Key = %s", s)
	}
}

func TestKey_RoundTripsThroughJSON(t *testing.T) {
	want := Key(vlog.HeadKey{Version: 1, PublicKey: []byte{9}})
	var got HeadKey
	if err := json.Unmarshal([]byte(encode(t, want)), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Version != want.Version || !bytes.Equal(got.PublicKey, want.PublicKey) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestCreate_CarriesTheIDAndPublicKeyNotTheKeyRef(t *testing.T) {
	got := Create(vlog.LogID{TenantID: "acme", Database: "ledger", Log: "l"}, &vlog.HeadKey{Version: 1, Ref: "software/v1", PublicKey: []byte{1, 2}})
	want := `{"tenant_id":"acme","database":"ledger","log":"l","key":{"version":1,"public_key":"AQI="}}`
	if s := encode(t, got); s != want {
		t.Fatalf("Create = %s, want %s", s, want)
	}
}

func TestCreate_NoKeyOmitsIt(t *testing.T) {
	got := Create(vlog.LogID{TenantID: "acme", Database: "ledger", Log: "l"}, nil)
	if got.Key != nil {
		t.Fatalf("Create with no key = %+v", got.Key)
	}
	if s := encode(t, got); s != `{"tenant_id":"acme","database":"ledger","log":"l"}` {
		t.Fatalf("Create with no key = %s", s)
	}
}

func TestList_NamesTheLogs(t *testing.T) {
	got := List([]vlog.LogID{{Log: "a"}, {Log: "b"}})
	if s := encode(t, got); s != `{"logs":["a","b"]}` {
		t.Fatalf("List = %s", s)
	}
}

func TestList_NoLogsIsAnEmptyList(t *testing.T) {
	if s := encode(t, List(nil)); s != `{"logs":[]}` {
		t.Fatalf("List of none = %s", s)
	}
}

func TestAppend_CarriesTheFirstIndexAndCount(t *testing.T) {
	if s := encode(t, &AppendResult{FirstIndex: 7, Count: 2}); s != `{"first_index":7,"count":2}` {
		t.Fatalf("Append = %s", s)
	}
}

func TestAppend_FirstEntryIsIndexZero(t *testing.T) {
	if s := encode(t, &AppendResult{Count: 1}); s != `{"first_index":0,"count":1}` {
		t.Fatalf("Append at zero = %s", s)
	}
}

func TestGet_BodiesAreBase64AndExpiredCarriesNone(t *testing.T) {
	got := Get([]vlog.Entry{{Index: 3, Body: []byte("hi")}, {Index: 4, Expired: true}})
	want := `{"entries":[{"index":3,"body":"aGk="},{"index":4,"expired":true}]}`
	if s := encode(t, got); s != want {
		t.Fatalf("Get = %s, want %s", s, want)
	}
}

func TestGet_NoEntriesIsAnEmptyList(t *testing.T) {
	if s := encode(t, Get(nil)); s != `{"entries":[]}` {
		t.Fatalf("Get of none = %s", s)
	}
}

func TestInclusion_CarriesIndexSizeAndHashes(t *testing.T) {
	got := &InclusionProof{Index: 4, Size: 9, Hashes: [][]byte{{1}, {2}}}
	if s := encode(t, got); s != `{"index":4,"size":9,"hashes":["AQ==","Ag=="]}` {
		t.Fatalf("Inclusion = %s", s)
	}
}

func TestInclusion_SingleLeafTreeHasNoHashes(t *testing.T) {
	got := &InclusionProof{Index: 0, Size: 1}
	if got.Index != 0 || got.Size != 1 || len(got.Hashes) != 0 {
		t.Fatalf("Inclusion = %+v", got)
	}
}

func TestConsistency_CarriesBothSizesAndHashes(t *testing.T) {
	got := &ConsistencyProof{Size1: 3, Size2: 9, Hashes: [][]byte{{1}}}
	if s := encode(t, got); s != `{"size1":3,"size2":9,"hashes":["AQ=="]}` {
		t.Fatalf("Consistency = %s", s)
	}
}

func TestConsistency_EqualSizesHaveNoHashes(t *testing.T) {
	got := &ConsistencyProof{Size1: 5, Size2: 5}
	if got.Size1 != 5 || got.Size2 != 5 || len(got.Hashes) != 0 {
		t.Fatalf("Consistency = %+v", got)
	}
}

func TestHeadOf_CarriesTheHeadAndCheckpoint(t *testing.T) {
	got := HeadOf(vlog.Head{Size: 9, Root: []byte{1}, BodiesFrom: 2, Checkpoint: []byte{2}, CheckpointSize: 8})
	if s := encode(t, got); s != `{"size":9,"root":"AQ==","bodies_from":2,"checkpoint":"Ag==","checkpoint_size":8}` {
		t.Fatalf("HeadOf = %s", s)
	}
}

func TestHeadOf_NoCheckpointOmitsIt(t *testing.T) {
	got := HeadOf(vlog.Head{Size: 1, Root: []byte{1}})
	if s := encode(t, got); s != `{"size":1,"root":"AQ==","bodies_from":0,"checkpoint_size":0}` {
		t.Fatalf("HeadOf = %s", s)
	}
}

func TestSignHead_CarriesTheSignatureType(t *testing.T) {
	got := &SignHeadResult{
		Head:       vlog.SignedHead{Size: 9, Root: []byte{1}, KeyVersion: 2, SignatureType: "ecdsa-p256", Signature: []byte{3}},
		Checkpoint: []byte("note"),
	}
	want := `{"head":{"size":9,"root":"AQ==","key_version":2,"signature_type":"ecdsa-p256","signature":"Aw=="},"checkpoint":"bm90ZQ=="}`
	if s := encode(t, got); s != want {
		t.Fatalf("SignHead = %s, want %s", s, want)
	}
}

func TestSignHead_CheckpointIsTheNoteUnchanged(t *testing.T) {
	note := []byte("origin\n9\nroot\n\n— sig\n")
	var got SignHeadResult
	if err := json.Unmarshal([]byte(encode(t, &SignHeadResult{Checkpoint: note})), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !bytes.Equal(got.Checkpoint, note) {
		t.Fatalf("SignHead checkpoint = %q", got.Checkpoint)
	}
}

func TestExpire_CarriesTheCount(t *testing.T) {
	if s := encode(t, &ExpireResult{Expired: 4}); s != `{"expired":4}` {
		t.Fatalf("Expire = %s", s)
	}
}

func TestExpire_NothingExpiredIsZero(t *testing.T) {
	if s := encode(t, &ExpireResult{}); s != `{"expired":0}` {
		t.Fatalf("Expire of none = %s", s)
	}
}
