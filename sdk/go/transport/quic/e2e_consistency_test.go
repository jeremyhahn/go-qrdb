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

package quic

import (
	"context"
	"sync/atomic"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// KV Put: write-mode wire encoding
// ---------------------------------------------------------------------------

func TestE2E_Put_WritesWriteModeField_WALDurable(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotMode atomic.Int32
	srv.SetResponder("kv.Put", func(req []byte) (uint16, []byte) {
		var r pb.PutRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotMode.Store(int32(r.GetWriteMode()))
		return wire.StatusOK, mustMarshal(&pb.PutResponse{})
	})

	if err := tr.Put(context.Background(), "k", []byte("v"), transport.WithWriteMode(transport.WriteModeWALDurable)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if pb.WriteMode(gotMode.Load()) != pb.WriteMode_WRITE_MODE_WAL_DURABLE {
		t.Errorf("WriteMode = %v, want WRITE_MODE_WAL_DURABLE", pb.WriteMode(gotMode.Load()))
	}
}

func TestE2E_Put_NoOpts_WriteModeIsUnspecified(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotMode atomic.Int32
	srv.SetResponder("kv.Put", func(req []byte) (uint16, []byte) {
		var r pb.PutRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotMode.Store(int32(r.GetWriteMode()))
		return wire.StatusOK, mustMarshal(&pb.PutResponse{})
	})

	if err := tr.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if pb.WriteMode(gotMode.Load()) != pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		t.Errorf("WriteMode = %v, want WRITE_MODE_UNSPECIFIED", pb.WriteMode(gotMode.Load()))
	}
}

// ---------------------------------------------------------------------------
// KV Get: consistency wire encoding
// ---------------------------------------------------------------------------

func TestE2E_Get_WritesConsistencyField_Strong(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotConsistency atomic.Int32
	srv.SetResponder("kv.Get", func(req []byte) (uint16, []byte) {
		var r pb.GetRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotConsistency.Store(int32(r.GetConsistency()))
		return wire.StatusOK, mustMarshal(&pb.GetResponse{Value: []byte("v")})
	})

	if _, err := tr.Get(context.Background(), "k", transport.WithConsistency(transport.ConsistencyStrong)); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if pb.ConsistencyLevel(gotConsistency.Load()) != pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG {
		t.Errorf("ConsistencyLevel = %v, want CONSISTENCY_LEVEL_STRONG", pb.ConsistencyLevel(gotConsistency.Load()))
	}
}

func TestE2E_Get_NoOpts_ConsistencyIsUnspecified(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotConsistency atomic.Int32
	srv.SetResponder("kv.Get", func(req []byte) (uint16, []byte) {
		var r pb.GetRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotConsistency.Store(int32(r.GetConsistency()))
		return wire.StatusOK, mustMarshal(&pb.GetResponse{Value: []byte("v")})
	})

	if _, err := tr.Get(context.Background(), "k"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if pb.ConsistencyLevel(gotConsistency.Load()) != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Errorf("ConsistencyLevel = %v, want CONSISTENCY_LEVEL_UNSPECIFIED", pb.ConsistencyLevel(gotConsistency.Load()))
	}
}

func TestE2E_Get_WithAllowStale_ConsistencyIsEventual(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotConsistency atomic.Int32
	srv.SetResponder("kv.Get", func(req []byte) (uint16, []byte) {
		var r pb.GetRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotConsistency.Store(int32(r.GetConsistency()))
		return wire.StatusOK, mustMarshal(&pb.GetResponse{Value: []byte("v")})
	})

	if _, err := tr.Get(context.Background(), "k", transport.WithAllowStale()); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if pb.ConsistencyLevel(gotConsistency.Load()) != pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL {
		t.Errorf("ConsistencyLevel = %v, want CONSISTENCY_LEVEL_EVENTUAL", pb.ConsistencyLevel(gotConsistency.Load()))
	}
}

// ---------------------------------------------------------------------------
// KV Delete: write-mode wire encoding
// ---------------------------------------------------------------------------

func TestE2E_Delete_WritesWriteModeField_Batched(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotMode atomic.Int32
	srv.SetResponder("kv.Delete", func(req []byte) (uint16, []byte) {
		var r pb.DeleteRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotMode.Store(int32(r.GetWriteMode()))
		return wire.StatusOK, mustMarshal(&pb.DeleteResponse{})
	})

	if err := tr.Delete(context.Background(), "k", transport.WithWriteMode(transport.WriteModeBatched)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if pb.WriteMode(gotMode.Load()) != pb.WriteMode_WRITE_MODE_BATCHED {
		t.Errorf("WriteMode = %v, want WRITE_MODE_BATCHED", pb.WriteMode(gotMode.Load()))
	}
}

func TestE2E_Delete_NoOpts_WriteModeIsUnspecified(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotMode atomic.Int32
	srv.SetResponder("kv.Delete", func(req []byte) (uint16, []byte) {
		var r pb.DeleteRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotMode.Store(int32(r.GetWriteMode()))
		return wire.StatusOK, mustMarshal(&pb.DeleteResponse{})
	})

	if err := tr.Delete(context.Background(), "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if pb.WriteMode(gotMode.Load()) != pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		t.Errorf("WriteMode = %v, want WRITE_MODE_UNSPECIFIED", pb.WriteMode(gotMode.Load()))
	}
}

// ---------------------------------------------------------------------------
// KV List: consistency wire encoding
// ---------------------------------------------------------------------------

func TestE2E_List_WritesConsistencyField_Linearizable(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotConsistency atomic.Int32
	srv.SetResponder("kv.List", func(req []byte) (uint16, []byte) {
		var r pb.ListRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotConsistency.Store(int32(r.GetConsistency()))
		return wire.StatusOK, mustMarshal(&pb.ListResponse{Keys: []string{"a"}})
	})

	if _, err := tr.List(context.Background(), "pref/", transport.WithConsistency(transport.ConsistencyLinearizable)); err != nil {
		t.Fatalf("List: %v", err)
	}
	if pb.ConsistencyLevel(gotConsistency.Load()) != pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE {
		t.Errorf("ConsistencyLevel = %v, want CONSISTENCY_LEVEL_LINEARIZABLE", pb.ConsistencyLevel(gotConsistency.Load()))
	}
}

func TestE2E_List_NoOpts_ConsistencyIsUnspecified(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotConsistency atomic.Int32
	srv.SetResponder("kv.List", func(req []byte) (uint16, []byte) {
		var r pb.ListRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotConsistency.Store(int32(r.GetConsistency()))
		return wire.StatusOK, mustMarshal(&pb.ListResponse{})
	})

	if _, err := tr.List(context.Background(), "pref/"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if pb.ConsistencyLevel(gotConsistency.Load()) != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Errorf("ConsistencyLevel = %v, want CONSISTENCY_LEVEL_UNSPECIFIED", pb.ConsistencyLevel(gotConsistency.Load()))
	}
}

// ---------------------------------------------------------------------------
// KV Scan: consistency wire encoding
// ---------------------------------------------------------------------------

func TestE2E_Scan_WritesConsistencyField_Eventual(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotConsistency atomic.Int32
	srv.SetResponder("kv.Scan", func(req []byte) (uint16, []byte) {
		var r pb.ScanRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotConsistency.Store(int32(r.GetConsistency()))
		return wire.StatusOK, mustMarshal(&pb.ScanResponse{})
	})

	if _, err := tr.Scan(context.Background(), "ns/", transport.WithConsistency(transport.ConsistencyEventual)); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if pb.ConsistencyLevel(gotConsistency.Load()) != pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL {
		t.Errorf("ConsistencyLevel = %v, want CONSISTENCY_LEVEL_EVENTUAL", pb.ConsistencyLevel(gotConsistency.Load()))
	}
}

func TestE2E_Scan_NoOpts_ConsistencyIsUnspecified(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotConsistency atomic.Int32
	srv.SetResponder("kv.Scan", func(req []byte) (uint16, []byte) {
		var r pb.ScanRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotConsistency.Store(int32(r.GetConsistency()))
		return wire.StatusOK, mustMarshal(&pb.ScanResponse{})
	})

	if _, err := tr.Scan(context.Background(), "ns/"); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if pb.ConsistencyLevel(gotConsistency.Load()) != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Errorf("ConsistencyLevel = %v, want CONSISTENCY_LEVEL_UNSPECIFIED", pb.ConsistencyLevel(gotConsistency.Load()))
	}
}

// ---------------------------------------------------------------------------
// MVCC PutWithLease: write-mode wire encoding
// ---------------------------------------------------------------------------

func TestE2E_PutWithLease_WritesWriteModeField_WALDurable(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotMode atomic.Int32
	srv.SetResponder("mvcc.PutWithLease", func(req []byte) (uint16, []byte) {
		var r pb.PutWithLeaseRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotMode.Store(int32(r.GetWriteMode()))
		return wire.StatusOK, mustMarshal(&pb.PutWithLeaseResponse{
			Kv: &pb.KeyValue{Key: []byte("k"), Version: 1},
		})
	})

	if _, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 1,
		transport.WithWriteMode(transport.WriteModeWALDurable)); err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	if pb.WriteMode(gotMode.Load()) != pb.WriteMode_WRITE_MODE_WAL_DURABLE {
		t.Errorf("WriteMode = %v, want WRITE_MODE_WAL_DURABLE", pb.WriteMode(gotMode.Load()))
	}
}

func TestE2E_PutWithLease_NoOpts_WriteModeIsUnspecified(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotMode atomic.Int32
	srv.SetResponder("mvcc.PutWithLease", func(req []byte) (uint16, []byte) {
		var r pb.PutWithLeaseRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotMode.Store(int32(r.GetWriteMode()))
		return wire.StatusOK, mustMarshal(&pb.PutWithLeaseResponse{
			Kv: &pb.KeyValue{Key: []byte("k"), Version: 1},
		})
	})

	if _, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 1); err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	if pb.WriteMode(gotMode.Load()) != pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		t.Errorf("WriteMode = %v, want WRITE_MODE_UNSPECIFIED", pb.WriteMode(gotMode.Load()))
	}
}

// ---------------------------------------------------------------------------
// MVCC GetRevision: consistency wire encoding
// ---------------------------------------------------------------------------

func TestE2E_GetRevision_WritesConsistencyField_Strong(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotConsistency atomic.Int32
	srv.SetResponder("mvcc.GetRevision", func(req []byte) (uint16, []byte) {
		var r pb.GetRevisionRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotConsistency.Store(int32(r.GetConsistency()))
		return wire.StatusOK, mustMarshal(&pb.GetRevisionResponse{
			Kv: &pb.KeyValue{Key: []byte("k"), Version: 1},
		})
	})

	if _, err := tr.GetRevision(context.Background(), "k",
		transport.WithConsistency(transport.ConsistencyStrong)); err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if pb.ConsistencyLevel(gotConsistency.Load()) != pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG {
		t.Errorf("ConsistencyLevel = %v, want CONSISTENCY_LEVEL_STRONG", pb.ConsistencyLevel(gotConsistency.Load()))
	}
}

func TestE2E_GetRevision_NoOpts_ConsistencyIsUnspecified(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotConsistency atomic.Int32
	srv.SetResponder("mvcc.GetRevision", func(req []byte) (uint16, []byte) {
		var r pb.GetRevisionRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotConsistency.Store(int32(r.GetConsistency()))
		return wire.StatusOK, mustMarshal(&pb.GetRevisionResponse{
			Kv: &pb.KeyValue{Key: []byte("k"), Version: 1},
		})
	})

	if _, err := tr.GetRevision(context.Background(), "k"); err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if pb.ConsistencyLevel(gotConsistency.Load()) != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Errorf("ConsistencyLevel = %v, want CONSISTENCY_LEVEL_UNSPECIFIED", pb.ConsistencyLevel(gotConsistency.Load()))
	}
}

// ---------------------------------------------------------------------------
// MVCC GetAtRevision: consistency wire encoding
// ---------------------------------------------------------------------------

func TestE2E_GetAtRevision_WritesConsistencyField_Eventual(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotConsistency atomic.Int32
	srv.SetResponder("mvcc.GetAtRevision", func(req []byte) (uint16, []byte) {
		var r pb.GetAtRevisionRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotConsistency.Store(int32(r.GetConsistency()))
		return wire.StatusOK, mustMarshal(&pb.GetAtRevisionResponse{
			Kv: &pb.KeyValue{Key: []byte("k"), Version: 5},
		})
	})

	if _, err := tr.GetAtRevision(context.Background(), "k", 5,
		transport.WithConsistency(transport.ConsistencyEventual)); err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	if pb.ConsistencyLevel(gotConsistency.Load()) != pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL {
		t.Errorf("ConsistencyLevel = %v, want CONSISTENCY_LEVEL_EVENTUAL", pb.ConsistencyLevel(gotConsistency.Load()))
	}
}

func TestE2E_GetAtRevision_NoOpts_ConsistencyIsUnspecified(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotConsistency atomic.Int32
	srv.SetResponder("mvcc.GetAtRevision", func(req []byte) (uint16, []byte) {
		var r pb.GetAtRevisionRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotConsistency.Store(int32(r.GetConsistency()))
		return wire.StatusOK, mustMarshal(&pb.GetAtRevisionResponse{
			Kv: &pb.KeyValue{Key: []byte("k"), Version: 5},
		})
	})

	if _, err := tr.GetAtRevision(context.Background(), "k", 5); err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	if pb.ConsistencyLevel(gotConsistency.Load()) != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Errorf("ConsistencyLevel = %v, want CONSISTENCY_LEVEL_UNSPECIFIED", pb.ConsistencyLevel(gotConsistency.Load()))
	}
}

// ---------------------------------------------------------------------------
// MVCC Range: consistency wire encoding
// ---------------------------------------------------------------------------

func TestE2E_Range_WritesConsistencyField_Linearizable(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotConsistency atomic.Int32
	srv.SetResponder("mvcc.Range", func(req []byte) (uint16, []byte) {
		var r pb.RangeRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotConsistency.Store(int32(r.GetConsistency()))
		return wire.StatusOK, mustMarshal(&pb.RangeResponse{Count: 0})
	})

	_, err := tr.Range(context.Background(), []byte("a"), []byte("z"),
		transport.RangeOptions{},
		transport.WithConsistency(transport.ConsistencyLinearizable))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if pb.ConsistencyLevel(gotConsistency.Load()) != pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE {
		t.Errorf("ConsistencyLevel = %v, want CONSISTENCY_LEVEL_LINEARIZABLE", pb.ConsistencyLevel(gotConsistency.Load()))
	}
}

func TestE2E_Range_NoOpts_ConsistencyIsUnspecified(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotConsistency atomic.Int32
	srv.SetResponder("mvcc.Range", func(req []byte) (uint16, []byte) {
		var r pb.RangeRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		gotConsistency.Store(int32(r.GetConsistency()))
		return wire.StatusOK, mustMarshal(&pb.RangeResponse{Count: 0})
	})

	_, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if pb.ConsistencyLevel(gotConsistency.Load()) != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Errorf("ConsistencyLevel = %v, want CONSISTENCY_LEVEL_UNSPECIFIED", pb.ConsistencyLevel(gotConsistency.Load()))
	}
}

// ---------------------------------------------------------------------------
// protoConsistency / protoWriteMode helper unit tests
// ---------------------------------------------------------------------------

func TestProtoWriteMode_Sync(t *testing.T) {
	got := protoWriteMode(transport.WithWriteMode(transport.WriteModeSync))
	if got != pb.WriteMode_WRITE_MODE_SYNC {
		t.Errorf("got %v, want SYNC", got)
	}
}
