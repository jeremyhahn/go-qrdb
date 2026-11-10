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

package grpc

import (
	"bytes"
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestProtoToWatchEvent_Nil verifies the nil-event guard branch.
func TestProtoToWatchEvent_Nil(t *testing.T) {
	if got := protoToWatchEvent(nil); got != nil {
		t.Errorf("protoToWatchEvent(nil) = %v, want nil", got)
	}
}

// TestProtoToWatchEvent_Put verifies field mapping for a PUT event with
// both current and previous key-values populated.
func TestProtoToWatchEvent_Put(t *testing.T) {
	ev := protoToWatchEvent(&pb.Event{
		Type: pb.EventType_EVENT_PUT,
		Kv: &pb.KeyValue{
			Key:            []byte("k"),
			Value:          []byte("v"),
			CreateRevision: 3,
			ModRevision:    7,
			Version:        2,
			LeaseId:        11,
		},
		PrevKv: &pb.KeyValue{Key: []byte("k"), Value: []byte("old")},
	})
	if ev == nil {
		t.Fatal("protoToWatchEvent returned nil for non-nil event")
	}
	if ev.Type != transport.EventPut {
		t.Errorf("Type = %v, want EventPut", ev.Type)
	}
	if ev.Kv == nil || string(ev.Kv.Key) != "k" || string(ev.Kv.Value) != "v" {
		t.Errorf("Kv = %+v, want key=k value=v", ev.Kv)
	}
	if ev.Kv.CreateRevision != 3 || ev.Kv.ModRevision != 7 || ev.Kv.Version != 2 || ev.Kv.LeaseID != 11 {
		t.Errorf("Kv revisions = %+v, want create=3 mod=7 version=2 lease=11", ev.Kv)
	}
	if ev.PrevKv == nil || string(ev.PrevKv.Value) != "old" {
		t.Errorf("PrevKv = %+v, want value=old", ev.PrevKv)
	}
}

// TestProtoToWatchEvent_DeleteNilKVs verifies a DELETE event with nil
// key-values maps to nil Kv/PrevKv.
func TestProtoToWatchEvent_DeleteNilKVs(t *testing.T) {
	ev := protoToWatchEvent(&pb.Event{Type: pb.EventType_EVENT_DELETE})
	if ev == nil {
		t.Fatal("protoToWatchEvent returned nil for non-nil event")
	}
	if ev.Type != transport.EventDelete {
		t.Errorf("Type = %v, want EventDelete", ev.Type)
	}
	if ev.Kv != nil || ev.PrevKv != nil {
		t.Errorf("Kv = %v, PrevKv = %v, want both nil", ev.Kv, ev.PrevKv)
	}
}

// TestAnyToString_String verifies that string values pass through verbatim.
func TestAnyToString_String(t *testing.T) {
	if got := anyToString("plain"); got != "plain" {
		t.Errorf("anyToString(string) = %q, want %q", got, "plain")
	}
}

// TestAnyToString_NonString verifies that non-string values are JSON-encoded.
func TestAnyToString_NonString(t *testing.T) {
	if got := anyToString(42); got != "42" {
		t.Errorf("anyToString(int) = %q, want %q", got, "42")
	}
	if got := anyToString(map[string]int{"a": 1}); got != `{"a":1}` {
		t.Errorf("anyToString(map) = %q, want %q", got, `{"a":1}`)
	}
}

// TestPrefixRangeEnd verifies the watch range end for a prefix: the last
// byte below 0xff is incremented and everything after it dropped, an
// all-0xff prefix maps to the no-upper-bound sentinel, and an empty prefix
// has no range end.
func TestPrefixRangeEnd(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix []byte
		want   []byte
	}{
		{"Empty", nil, nil},
		{"Increments", []byte("a"), []byte("b")},
		{"DropsTrailingFF", []byte{'a', 0xff}, []byte("b")},
		{"AllFF", []byte{0xff, 0xff}, []byte{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := prefixRangeEnd(tc.prefix); !bytes.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Errorf("prefixRangeEnd(%v) = %v, want %v", tc.prefix, got, tc.want)
			}
		})
	}
}

// TestPbUserFactorToTransport verifies every field of a user auth factor
// record crosses the wire, the optional timestamps included, and that an
// unset optional timestamp stays nil.
func TestPbUserFactorToTransport(t *testing.T) {
	registered := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	lastUsed := registered.Add(time.Hour)
	revoked := registered.Add(2 * time.Hour)
	got := pbUserFactorToTransport(&pb.UserAuthFactorRecord{
		Id:             "f1",
		UserId:         7,
		Type:           "fido2",
		Label:          "key",
		Credential:     []byte("cred"),
		SpkiCredential: []byte("spki"),
		Metadata:       map[string]string{"aaguid": "x"},
		CreatedBy:      "admin",
		RegisteredAt:   registered.Format(time.RFC3339),
		LastUsedAt:     lastUsed.Format(time.RFC3339),
		RevokedAt:      revoked.Format(time.RFC3339),
	})
	if got.ID != "f1" || got.UserID != 7 || got.Type != "fido2" || got.Label != "key" || got.CreatedBy != "admin" {
		t.Errorf("scalar fields = %+v", got)
	}
	if string(got.Credential) != "cred" || string(got.SPKICredential) != "spki" || got.Metadata["aaguid"] != "x" {
		t.Errorf("credential/metadata = %q/%q/%v", got.Credential, got.SPKICredential, got.Metadata)
	}
	if !got.RegisteredAt.Equal(registered) {
		t.Errorf("RegisteredAt = %v, want %v", got.RegisteredAt, registered)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(lastUsed) {
		t.Errorf("LastUsedAt = %v, want %v", got.LastUsedAt, lastUsed)
	}
	if got.RevokedAt == nil || !got.RevokedAt.Equal(revoked) {
		t.Errorf("RevokedAt = %v, want %v", got.RevokedAt, revoked)
	}

	unset := pbUserFactorToTransport(&pb.UserAuthFactorRecord{Id: "f2"})
	if unset.LastUsedAt != nil || unset.RevokedAt != nil {
		t.Errorf("unset timestamps = %v/%v, want nil/nil", unset.LastUsedAt, unset.RevokedAt)
	}
	if zero := pbUserFactorToTransport(nil); zero.ID != "" || zero.Credential != nil {
		t.Errorf("pbUserFactorToTransport(nil) = %+v, want the zero record", zero)
	}
}

func TestProtoToNamespaceInfo_IndexConfigAndMetadata(t *testing.T) {
	got, err := protoToNamespaceInfo(&pb.VectorNamespaceInfo{
		Namespace:      "ns",
		Dimension:      4,
		VectorCount:    3,
		DistanceMetric: pb.DistanceMetric_DISTANCE_COSINE,
		IndexType:      pb.VectorIndexType_INDEX_HNSW,
		IndexConfig:    map[string]string{"ef_construction": "321"},
		MetadataJson:   `{"owner":"team-a","rank":2}`,
	})
	if err != nil {
		t.Fatalf("protoToNamespaceInfo: %v", err)
	}
	if got.Namespace != "ns" || got.Dimension != 4 || got.VectorCount != 3 || got.IndexType != transport.IndexHNSW {
		t.Fatalf("got %+v", got)
	}
	if got.IndexConfig["ef_construction"] != "321" {
		t.Fatalf("IndexConfig = %v, want ef_construction=321", got.IndexConfig)
	}
	if got.Metadata["owner"] != "team-a" || got.Metadata["rank"] != float64(2) {
		t.Fatalf("Metadata = %v, want owner=team-a rank=2", got.Metadata)
	}
}

func TestProtoToNamespaceInfo_InvalidMetadata(t *testing.T) {
	if _, err := protoToNamespaceInfo(&pb.VectorNamespaceInfo{Namespace: "ns", MetadataJson: "{bad-json"}); err == nil {
		t.Fatal("protoToNamespaceInfo accepted malformed metadata JSON")
	}
}

func TestTransportProtocolConfigToProto(t *testing.T) {
	t.Run("WithEtcd", func(t *testing.T) {
		got := transportProtocolConfigToProto(transport.ProtocolConfig{
			MySQL:      true,
			PostgreSQL: true,
			Etcd: &transport.EtcdProtocolConfig{
				Enabled: true, ListenAddr: ":2379", AdvertiseClientURLs: []string{"http://localhost:2379"},
			},
		})
		if !got.GetMysql() || !got.GetPostgresql() {
			t.Fatalf("mysql/postgresql = %v/%v, want true/true", got.GetMysql(), got.GetPostgresql())
		}
		etcd := got.GetEtcd()
		if etcd == nil || !etcd.GetEnabled() || etcd.GetListenAddr() != ":2379" ||
			len(etcd.GetAdvertiseClientUrls()) != 1 || etcd.GetAdvertiseClientUrls()[0] != "http://localhost:2379" {
			t.Fatalf("etcd = %+v", etcd)
		}
	})
	t.Run("WithoutEtcd", func(t *testing.T) {
		got := transportProtocolConfigToProto(transport.ProtocolConfig{MySQL: true})
		if !got.GetMysql() || got.GetPostgresql() || got.GetEtcd() != nil {
			t.Fatalf("got %+v, want mysql only and no etcd", got)
		}
	})
}
