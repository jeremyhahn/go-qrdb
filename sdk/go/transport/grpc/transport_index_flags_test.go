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
	"context"
	"net"
	"sync"
	"testing"

	googlegrpc "google.golang.org/grpc"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	qrdbindex "github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// recordingIndexServicer captures the index.IndexConfig the gRPC server hands
// to the service layer, so a test can assert what the request actually became
// rather than only that the call succeeded.
type recordingIndexServicer struct {
	mu             sync.Mutex
	cfg            qrdbindex.IndexConfig
	scanStart      []byte
	scanEnd        []byte
	scanStartIsNil bool
	scanEndIsNil   bool
}

var _ qrdbindex.IndexServicer = (*recordingIndexServicer)(nil)

func (r *recordingIndexServicer) CreateIndex(_ context.Context, _, _ string, cfg qrdbindex.IndexConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
	return nil
}
func (r *recordingIndexServicer) DropIndex(context.Context, string) error { return nil }
func (r *recordingIndexServicer) Query(context.Context, string, []byte) ([][]byte, error) {
	return nil, nil
}
func (r *recordingIndexServicer) Scan(_ context.Context, _ string, start, end []byte) (map[string][][]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scanStart, r.scanEnd = start, end
	r.scanStartIsNil, r.scanEndIsNil = start == nil, end == nil
	// Return a non-empty result so a caller can also assert a one-sided range
	// is not silently empty.
	return map[string][][]byte{"beta": {[]byte("k1")}}, nil
}
func (r *recordingIndexServicer) ListIndexes(context.Context) ([]string, error) { return nil, nil }

func (r *recordingIndexServicer) config() qrdbindex.IndexConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg
}

// startIndexFlagServer runs the REAL pkg/api/grpc service over a loopback
// listener, so the assertions below cover the client, the wire message and
// the server's request-to-config mapping in one pass. A double on either side
// would let a field vanish between them.
func startIndexFlagServer(t *testing.T) (*recordingIndexServicer, *Transport) {
	t.Helper()
	rec := &recordingIndexServicer{}
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterIndex(rec))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	tr, cleanup := connectLLITransport(t, lis.Addr().String())
	t.Cleanup(cleanup)
	return rec, tr
}

// TestGRPCIndexCreate_FlagsSurviveTheRoundTrip covers go-qrdb#34 end to end on
// the gRPC plane.
//
// IndexCreateRequest carried name, field, unique and sparse. Numeric was a
// transport.IndexConfig field the message could not express, and
// case-insensitivity could not be expressed at all — so sdk/go's kvStoreAdapter
// refused a case-insensitive definition rather than create a case-SENSITIVE
// index and report success.
func TestGRPCIndexCreate_FlagsSurviveTheRoundTrip(t *testing.T) {
	rec, tr := startIndexFlagServer(t)

	err := tr.IndexCreate(context.Background(), "by_email", "email", transport.IndexConfig{
		Unique:          true,
		Sparse:          true,
		Numeric:         true,
		CaseInsensitive: true,
	})
	if err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}

	got := rec.config()
	if !got.Unique {
		t.Error("Unique lost between client and service")
	}
	if !got.Sparse {
		t.Error("Sparse lost between client and service")
	}
	if got.ValueType != qrdbindex.IndexValueTypeNumeric {
		t.Errorf("ValueType = %v; want numeric — the flag never reached the service", got.ValueType)
	}
	if got.CaseSensitive {
		t.Error("a case-insensitive request produced CaseSensitive:true; the engine " +
			"would build a case-SENSITIVE index and answer a folded lookup empty " +
			"with a nil error")
	}
}

// TestGRPCIndexCreate_ZeroConfigIsTheDefaultIndex is the control and the
// polarity guard.
//
// index.IndexConfig names CaseSensitive for the DEFAULT, so its zero value
// folds case. The handler must therefore set it explicitly: a handler that
// simply omitted the field would turn every ordinary gRPC-created index
// case-insensitive, and nothing else in the suite would notice.
func TestGRPCIndexCreate_ZeroConfigIsTheDefaultIndex(t *testing.T) {
	rec, tr := startIndexFlagServer(t)

	if err := tr.IndexCreate(context.Background(), "by_name", "name", transport.IndexConfig{}); err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}

	got := rec.config()
	if !got.CaseSensitive {
		t.Error("a zero config produced a case-INSENSITIVE index; the default must " +
			"stay case-sensitive")
	}
	if got.Unique || got.Sparse {
		t.Errorf("a zero config produced %+v; want no claims", got)
	}
	if got.ValueType == qrdbindex.IndexValueTypeNumeric {
		t.Error("a zero config produced a numeric index")
	}
}

// TestIndexCreateRequest_CarriesTheNewFields pins the generated message
// itself. A regenerated proto that dropped or renumbered these fields would
// break the round trip above in a way that reads like a mapping bug.
func TestIndexCreateRequest_CarriesTheNewFields(t *testing.T) {
	req := &pb.IndexCreateRequest{Numeric: true, CaseInsensitive: true}
	if !req.GetNumeric() {
		t.Error("IndexCreateRequest.numeric is not readable")
	}
	if !req.GetCaseInsensitive() {
		t.Error("IndexCreateRequest.case_insensitive is not readable")
	}
	if (&pb.IndexCreateRequest{}).GetCaseInsensitive() {
		t.Error("an unset case_insensitive read as true; absent must mean case-SENSITIVE")
	}
}

func (r *recordingIndexServicer) scanBounds() (start, end []byte, startNil, endNil bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.scanStart, r.scanEnd, r.scanStartIsNil, r.scanEndIsNil
}

// TestGRPCIndexScan_OpenBoundsSurviveTheRoundTrip covers go-qrdb#36 end to end
// on the gRPC plane. Before the fix the transport signature was plain strings,
// so a nil (open) bound arrived at the service as "" — an explicit empty bound
// — and a one-sided range such as Team >= "beta" scanned the empty interval
// ["beta", "") and answered empty with a nil error. The *string contract plus
// proto3 optional presence must now carry a nil bound through the client, the
// wire and the server untouched.
func TestGRPCIndexScan_OpenBoundsSurviveTheRoundTrip(t *testing.T) {
	t.Run("open upper", func(t *testing.T) {
		rec, tr := startIndexFlagServer(t)
		resp, err := tr.IndexScan(context.Background(), "by_team", strPtrG("beta"), nil)
		if err != nil {
			t.Fatalf("IndexScan: %v", err)
		}
		if len(resp.Entries) == 0 {
			t.Fatal("a one-sided range over an index with matches must not be empty")
		}
		start, _, startNil, endNil := rec.scanBounds()
		if startNil {
			t.Error("start reached the service as nil; a present lower bound was lost")
		}
		if string(start) != "beta" {
			t.Errorf("start = %q; want %q", start, "beta")
		}
		if !endNil {
			t.Error("end reached the service non-nil; an open upper bound must stay nil")
		}
	})

	t.Run("open lower", func(t *testing.T) {
		rec, tr := startIndexFlagServer(t)
		if _, err := tr.IndexScan(context.Background(), "by_team", nil, strPtrG("z")); err != nil {
			t.Fatalf("IndexScan: %v", err)
		}
		_, end, startNil, endNil := rec.scanBounds()
		if !startNil {
			t.Error("start reached the service non-nil; an open lower bound must stay nil")
		}
		if endNil || string(end) != "z" {
			t.Errorf("end = %q (nil=%v); want %q present", end, endNil, "z")
		}
	})

	t.Run("present empty is not open", func(t *testing.T) {
		rec, tr := startIndexFlagServer(t)
		empty := ""
		if _, err := tr.IndexScan(context.Background(), "by_team", &empty, strPtrG("z")); err != nil {
			t.Fatalf("IndexScan: %v", err)
		}
		start, _, startNil, _ := rec.scanBounds()
		if startNil {
			t.Error("an explicit empty start reached the service as nil; present-empty must stay distinct from open")
		}
		if len(start) != 0 {
			t.Errorf("start = %q; want present-empty", start)
		}
	})
}

func strPtrG(s string) *string { return &s }
