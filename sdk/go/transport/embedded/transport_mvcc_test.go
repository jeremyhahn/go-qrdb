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

package embedded

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Mock: MVCCKVServicer
// ===========================================================================

type mockMVCCService struct {
	*mockKVService
	currentRev int64
}

func newMockMVCCService() *mockMVCCService {
	return &mockMVCCService{
		mockKVService: newMockKVService(),
		currentRev:    1,
	}
}

func (m *mockMVCCService) PutWithLease(_ context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	return &kv.KeyValue{
		Key:         []byte(key),
		Value:       value,
		LeaseID:     leaseID,
		ModRevision: m.currentRev,
		Version:     1,
	}, nil
}

func (m *mockMVCCService) PutWithLeaseAndMode(_ context.Context, key string, value []byte, leaseID int64, _ writemode.Mode) (*kv.KeyValue, error) {
	return &kv.KeyValue{
		Key:         []byte(key),
		Value:       value,
		LeaseID:     leaseID,
		ModRevision: m.currentRev,
		Version:     1,
	}, nil
}

func (m *mockMVCCService) GetRevision(_ context.Context, key string) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: 1, Version: 1}, nil
}

func (m *mockMVCCService) GetRevisionWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: 1, Version: 1}, nil
}

func (m *mockMVCCService) GetAtRevision(_ context.Context, key string, revision int64) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: revision, Version: 1}, nil
}

func (m *mockMVCCService) GetAtRevisionWithConsistency(_ context.Context, key string, revision int64, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: revision, Version: 1}, nil
}

func (m *mockMVCCService) Range(_ context.Context, start, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	return []*kv.KeyValue{
		{Key: start, Value: []byte("v"), ModRevision: 1, Version: 1},
	}, 1, 77, nil
}

func (m *mockMVCCService) RangeWithConsistency(_ context.Context, start, _ []byte, _ consistency.ReadOptions, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	return []*kv.KeyValue{
		{Key: start, Value: []byte("v"), ModRevision: 1, Version: 1},
	}, 1, 77, nil
}

func (m *mockMVCCService) Compact(_ context.Context, _ int64) error {
	return nil
}

func (m *mockMVCCService) CurrentRevision(_ context.Context) (int64, error) {
	return m.currentRev, nil
}

func (m *mockMVCCService) RestoreToRevision(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	return &service.RestoreRevisionResult{
		Revision: req.Revision,
		Changes: []service.RestoreKeyChange{
			{Key: "restored-key", Action: service.RestoreActionRevert, FromRevision: 5, ToRevision: 3, ValueBytes: 7},
		},
		Applied: 1,
		DryRun:  req.DryRun,
	}, nil
}

var _ service.MVCCKVServicer = (*mockMVCCService)(nil)

// errMVCCRefused is the failure recordingMVCCService answers with when told to.
var errMVCCRefused = errors.New("mvcc: refused")

// mvccSent is what one MVCC service call received.
type mvccSent struct {
	method    string
	key       string
	end       string
	value     string
	number    int64 // the lease of a put, the revision of a read or a compaction
	mode      writemode.Mode
	read      consistency.Level
	rangeOpts service.RangeOptions
	restore   *service.RestoreRevisionRequest
}

// recordingMVCCService keeps what the last MVCC call received and answers
// with every key-value field set, each to a distinct value, or with err when
// it is non-nil.
type recordingMVCCService struct {
	*mockMVCCService
	err  error
	sent mvccSent
}

func newRecordingMVCCService(err error) *recordingMVCCService {
	return &recordingMVCCService{mockMVCCService: newMockMVCCService(), err: err}
}

func (r *recordingMVCCService) answer(s mvccSent) (*kv.KeyValue, error) {
	r.sent = s
	if r.err != nil {
		return nil, r.err
	}
	return &kv.KeyValue{Key: []byte(s.key), Value: []byte("v"), CreateRevision: 2, ModRevision: 3, Version: 4, LeaseID: 5}, nil
}

func (r *recordingMVCCService) PutWithLease(_ context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	return r.answer(mvccSent{method: "PutWithLease", key: key, value: string(value), number: leaseID})
}

func (r *recordingMVCCService) PutWithLeaseAndMode(_ context.Context, key string, value []byte, leaseID int64, mode writemode.Mode) (*kv.KeyValue, error) {
	return r.answer(mvccSent{method: "PutWithLeaseAndMode", key: key, value: string(value), number: leaseID, mode: mode})
}

func (r *recordingMVCCService) GetRevision(_ context.Context, key string) (*kv.KeyValue, error) {
	return r.answer(mvccSent{method: "GetRevision", key: key})
}

func (r *recordingMVCCService) GetRevisionWithConsistency(_ context.Context, key string, ro consistency.ReadOptions) (*kv.KeyValue, error) {
	return r.answer(mvccSent{method: "GetRevisionWithConsistency", key: key, read: ro.Consistency})
}

func (r *recordingMVCCService) GetAtRevision(_ context.Context, key string, revision int64) (*kv.KeyValue, error) {
	return r.answer(mvccSent{method: "GetAtRevision", key: key, number: revision})
}

func (r *recordingMVCCService) GetAtRevisionWithConsistency(_ context.Context, key string, revision int64, ro consistency.ReadOptions) (*kv.KeyValue, error) {
	return r.answer(mvccSent{method: "GetAtRevisionWithConsistency", key: key, number: revision, read: ro.Consistency})
}

func (r *recordingMVCCService) rangeAnswer(s mvccSent) ([]*kv.KeyValue, int64, int64, error) {
	got, err := r.answer(s)
	if err != nil {
		return nil, 0, 0, err
	}
	return []*kv.KeyValue{got}, 6, 77, nil
}

func (r *recordingMVCCService) Range(_ context.Context, start, end []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	return r.rangeAnswer(mvccSent{method: "Range", key: string(start), end: string(end), rangeOpts: service.ApplyRangeOptions(opts)})
}

func (r *recordingMVCCService) RangeWithConsistency(_ context.Context, start, end []byte, ro consistency.ReadOptions, opts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	return r.rangeAnswer(mvccSent{
		method: "RangeWithConsistency", key: string(start), end: string(end), read: ro.Consistency,
		rangeOpts: service.ApplyRangeOptions(opts),
	})
}

func (r *recordingMVCCService) Compact(_ context.Context, revision int64) error {
	r.sent = mvccSent{method: "Compact", number: revision}
	return r.err
}

func (r *recordingMVCCService) CurrentRevision(context.Context) (int64, error) {
	r.sent = mvccSent{method: "CurrentRevision"}
	if r.err != nil {
		return 0, r.err
	}
	return 77, nil
}

func (r *recordingMVCCService) RestoreToRevision(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	r.sent = mvccSent{method: "RestoreToRevision", restore: req}
	if r.err != nil {
		return nil, r.err
	}
	return r.mockMVCCService.RestoreToRevision(ctx, req)
}

// requestAnswer sends method through Request and returns the answer it
// decoded into a fresh *T.
func requestAnswer[T any](ctx context.Context, tr *Transport, method string, req any) (any, error) {
	out := new(*T)
	err := tr.Request(ctx, method, req, out)
	return *out, err
}

// mvccCase is one MVCC operation: how to call it, what the service must
// receive, and the answer the transport must decode.
type mvccCase struct {
	name     string
	call     func(context.Context, *Transport) (any, error)
	wantSent mvccSent
	want     any
}

func mvccCases() []mvccCase {
	kvWant := func(key string) *transport.MVCCKeyValue {
		return &transport.MVCCKeyValue{Key: []byte(key), Value: []byte("v"), CreateRevision: 2, ModRevision: 3, Version: 4, LeaseID: 5}
	}
	rangeWant := &transport.RangeResponse{Kvs: []*transport.MVCCKeyValue{kvWant("a")}, Count: 6, Revision: 77}
	linearizable := transport.WithConsistency(transport.ConsistencyLinearizable)
	walDurable := transport.WithWriteMode(transport.WriteModeWALDurable)
	restoreReq := &transport.RestoreRevisionRequest{Revision: 5, Prefix: []byte("p"), Keys: [][]byte{[]byte("k")}, DryRun: true, Force: true}
	restoreSent := &service.RestoreRevisionRequest{Revision: 5, Prefix: "p", Keys: []string{"k"}, DryRun: true, Force: true}
	restoreWant := &transport.RestoreRevisionResult{
		Revision: 5,
		Changes: []*transport.RestoreKeyChange{
			{Key: []byte("restored-key"), Action: transport.RestoreActionRevert, FromRevision: 5, ToRevision: 3, ValueBytes: 7},
		},
		Conflicts: []*transport.RestoreKeyChange{},
		Applied:   1,
		DryRun:    true,
	}
	rangeOpts := transport.RangeOptions{Limit: 5, Revision: 9, CountOnly: true, KeysOnly: true, SortOrder: transport.SortDescend, SortTarget: transport.SortByModRevision}
	rangeOptsSent := service.RangeOptions{Limit: 5, Revision: 9, CountOnly: true, KeysOnly: true, SortOrder: service.SortDescend, SortTarget: service.SortByModRevision}

	return []mvccCase{
		{
			name: "PutWithLease",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return tr.PutWithLease(ctx, "k", []byte("val"), 42)
			},
			wantSent: mvccSent{method: "PutWithLease", key: "k", value: "val", number: 42},
			want:     kvWant("k"),
		},
		{
			name: "PutWithLease/WriteMode",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return tr.PutWithLease(ctx, "k", []byte("val"), 42, walDurable)
			},
			wantSent: mvccSent{method: "PutWithLeaseAndMode", key: "k", value: "val", number: 42, mode: writemode.WALDurable},
			want:     kvWant("k"),
		},
		{
			name: "PutWithLease/Request",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return requestAnswer[transport.MVCCKeyValue](ctx, tr, "PutWithLease", &putWithLeaseRequest{Key: "k", Value: []byte("val"), LeaseID: 42})
			},
			wantSent: mvccSent{method: "PutWithLease", key: "k", value: "val", number: 42},
			want:     kvWant("k"),
		},
		{
			name:     "GetRevision",
			call:     func(ctx context.Context, tr *Transport) (any, error) { return tr.GetRevision(ctx, "k") },
			wantSent: mvccSent{method: "GetRevision", key: "k"},
			want:     kvWant("k"),
		},
		{
			name:     "GetRevision/Consistency",
			call:     func(ctx context.Context, tr *Transport) (any, error) { return tr.GetRevision(ctx, "k", linearizable) },
			wantSent: mvccSent{method: "GetRevisionWithConsistency", key: "k", read: consistency.Linearizable},
			want:     kvWant("k"),
		},
		{
			name: "GetRevision/Request",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return requestAnswer[transport.MVCCKeyValue](ctx, tr, "GetRevision", &getRevisionRequest{Key: "k"})
			},
			wantSent: mvccSent{method: "GetRevision", key: "k"},
			want:     kvWant("k"),
		},
		{
			name:     "GetAtRevision",
			call:     func(ctx context.Context, tr *Transport) (any, error) { return tr.GetAtRevision(ctx, "k", 7) },
			wantSent: mvccSent{method: "GetAtRevision", key: "k", number: 7},
			want:     kvWant("k"),
		},
		{
			name: "GetAtRevision/Consistency",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return tr.GetAtRevision(ctx, "k", 7, linearizable)
			},
			wantSent: mvccSent{method: "GetAtRevisionWithConsistency", key: "k", number: 7, read: consistency.Linearizable},
			want:     kvWant("k"),
		},
		{
			name: "GetAtRevision/Request",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return requestAnswer[transport.MVCCKeyValue](ctx, tr, "GetAtRevision", &getAtRevisionRequest{Key: "k", Revision: 7})
			},
			wantSent: mvccSent{method: "GetAtRevision", key: "k", number: 7},
			want:     kvWant("k"),
		},
		{
			name: "Range",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return tr.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{})
			},
			wantSent: mvccSent{method: "Range", key: "a", end: "z"},
			want:     rangeWant,
		},
		{
			name: "Range/Options",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return tr.Range(ctx, []byte("a"), []byte("z"), rangeOpts)
			},
			wantSent: mvccSent{method: "Range", key: "a", end: "z", rangeOpts: rangeOptsSent},
			want:     rangeWant,
		},
		{
			name: "Range/Consistency",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return tr.Range(ctx, []byte("a"), []byte("z"), rangeOpts, linearizable)
			},
			wantSent: mvccSent{method: "RangeWithConsistency", key: "a", end: "z", read: consistency.Linearizable, rangeOpts: rangeOptsSent},
			want:     rangeWant,
		},
		{
			name: "Range/Request",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return requestAnswer[transport.RangeResponse](ctx, tr, "Range", &rangeRequest{Start: []byte("a"), End: []byte("z"), Opts: rangeOpts})
			},
			wantSent: mvccSent{method: "Range", key: "a", end: "z", rangeOpts: rangeOptsSent},
			want:     rangeWant,
		},
		{
			name:     "Compact",
			call:     func(ctx context.Context, tr *Transport) (any, error) { return nil, tr.Compact(ctx, 100) },
			wantSent: mvccSent{method: "Compact", number: 100},
		},
		{
			name: "Compact/Request",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return nil, tr.Request(ctx, "Compact", &compactRequest{Revision: 100}, nil)
			},
			wantSent: mvccSent{method: "Compact", number: 100},
		},
		{
			name:     "CurrentRevision",
			call:     func(ctx context.Context, tr *Transport) (any, error) { return tr.CurrentRevision(ctx) },
			wantSent: mvccSent{method: "CurrentRevision"},
			want:     int64(77),
		},
		{
			name: "CurrentRevision/Request",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				var rev int64
				err := tr.Request(ctx, "CurrentRevision", nil, &rev)
				return rev, err
			},
			wantSent: mvccSent{method: "CurrentRevision"},
			want:     int64(77),
		},
		{
			name:     "RestoreToRevision",
			call:     func(ctx context.Context, tr *Transport) (any, error) { return tr.RestoreToRevision(ctx, restoreReq) },
			wantSent: mvccSent{method: "RestoreToRevision", restore: restoreSent},
			want:     restoreWant,
		},
		{
			name: "RestoreToRevision/Request",
			call: func(ctx context.Context, tr *Transport) (any, error) {
				return requestAnswer[transport.RestoreRevisionResult](ctx, tr, "RestoreToRevision", restoreReq)
			},
			wantSent: mvccSent{method: "RestoreToRevision", restore: restoreSent},
			want:     restoreWant,
		},
	}
}

// TestEmbeddedMVCC_SendsTheDocumentedRequest checks what each MVCC operation,
// called directly or through Request and with each option variant, hands the
// service, and the answer it decodes from the service's.
func TestEmbeddedMVCC_SendsTheDocumentedRequest(t *testing.T) {
	for _, tc := range mvccCases() {
		t.Run(tc.name, func(t *testing.T) {
			svc := newRecordingMVCCService(nil)
			got, err := tc.call(context.Background(), newExtTransport(t, WithMVCCService(svc)))
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if !reflect.DeepEqual(svc.sent, tc.wantSent) {
				t.Errorf("service received %+v, want %+v", svc.sent, tc.wantSent)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("answer %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestEmbeddedMVCC_SurfacesAFailedAnswer checks that a service failure on
// every MVCC path comes back as that failure, with no answer.
func TestEmbeddedMVCC_SurfacesAFailedAnswer(t *testing.T) {
	for _, tc := range mvccCases() {
		t.Run(tc.name, func(t *testing.T) {
			svc := newRecordingMVCCService(errMVCCRefused)
			got, err := tc.call(context.Background(), newExtTransport(t, WithMVCCService(svc)))
			if !errors.Is(err, errMVCCRefused) {
				t.Fatalf("%s error = %v, want %v", tc.name, err, errMVCCRefused)
			}
			if svc.sent.method != tc.wantSent.method {
				t.Errorf("service method = %q, want %q", svc.sent.method, tc.wantSent.method)
			}
			if !noAnswer(got) {
				t.Errorf("%s answer = %+v, want none", tc.name, got)
			}
		})
	}
}

// TestEmbeddedMVCC_RestoreToRevision_NilRequest checks that a nil restore
// request is refused with the typed error before it reaches the service.
func TestEmbeddedMVCC_RestoreToRevision_NilRequest(t *testing.T) {
	svc := newRecordingMVCCService(nil)
	got, err := newExtTransport(t, WithMVCCService(svc)).RestoreToRevision(context.Background(), nil)
	var re *transport.RequestError
	var nre *transport.NilRequestError
	if !errors.As(err, &re) || re.Method != "RestoreToRevision" || !errors.As(err, &nre) {
		t.Fatalf("RestoreToRevision(nil) error = %T %v, want RequestError{RestoreToRevision, NilRequestError}", err, err)
	}
	if got != nil || svc.sent.method != "" {
		t.Errorf("answer %+v, service received %+v; want neither", got, svc.sent)
	}
}

// TestEmbeddedMVCC_RestoreToRevision_RequestWrongType checks that Request
// refuses a RestoreToRevision request of the wrong type with the typed error
// naming the method, before it reaches the service.
func TestEmbeddedMVCC_RestoreToRevision_RequestWrongType(t *testing.T) {
	svc := newRecordingMVCCService(nil)
	out := new(*transport.RestoreRevisionResult)
	err := newExtTransport(t, WithMVCCService(svc)).Request(context.Background(), "RestoreToRevision", transport.RestoreRevisionRequest{Revision: 5}, out)
	var ie *InvalidRequestError
	if !errors.As(err, &ie) || ie.Method != "RestoreToRevision" {
		t.Fatalf("Request(RestoreToRevision, value) error = %T %v, want InvalidRequestError{RestoreToRevision}", err, err)
	}
	if *out != nil || svc.sent.method != "" {
		t.Errorf("answer %+v, service received %+v; want neither", *out, svc.sent)
	}
}

// mvccCall is one MVCC or transaction operation.
type mvccCall struct {
	name string
	call func(context.Context, *Transport) (any, error)
}

func mvccCalls() []mvccCall {
	return []mvccCall{
		{"PutWithLease", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.PutWithLease(ctx, "k", []byte("v"), 1)
		}},
		{"GetRevision", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.GetRevision(ctx, "k")
		}},
		{"GetAtRevision", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.GetAtRevision(ctx, "k", 1)
		}},
		{"Range", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{})
		}},
		{"Compact", func(ctx context.Context, tr *Transport) (any, error) {
			return nil, tr.Compact(ctx, 1)
		}},
		{"CurrentRevision", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.CurrentRevision(ctx)
		}},
		{"RestoreToRevision", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.RestoreToRevision(ctx, &transport.RestoreRevisionRequest{Revision: 1})
		}},
		{"ExecuteTransaction", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.ExecuteTransaction(ctx, &transport.TransactionRequest{})
		}},
	}
}

// noAnswer reports whether got is the zero answer a refused call returns.
func noAnswer(got any) bool {
	v := reflect.ValueOf(got)
	return !v.IsValid() || v.IsZero()
}

// TestEmbeddedMVCC_NilService checks that every MVCC and transaction
// operation on a transport built without the MVCC service refuses with the
// typed error and no answer.
func TestEmbeddedMVCC_NilService(t *testing.T) {
	tr := newExtTransport(t)
	for _, tc := range mvccCalls() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call(context.Background(), tr)
			var ne *NilServiceError
			if !errors.As(err, &ne) {
				t.Fatalf("%s error = %T %v, want NilServiceError", tc.name, err, err)
			}
			if !noAnswer(got) {
				t.Errorf("%s answer = %+v, want none", tc.name, got)
			}
		})
	}
}

// TestEmbeddedMVCC_Closed checks that a closed transport refuses every MVCC
// and transaction operation with ClosedError and no answer, before the
// service sees it.
func TestEmbeddedMVCC_Closed(t *testing.T) {
	svc := newRecordingMVCCService(nil)
	tr := closedExtTransport(t, WithMVCCService(svc), WithTransactionService(&mockTxService{}))
	for _, tc := range mvccCalls() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call(context.Background(), tr)
			var ce *transport.ClosedError
			if !errors.As(err, &ce) {
				t.Fatalf("%s error = %T %v, want ClosedError", tc.name, err, err)
			}
			if !noAnswer(got) {
				t.Errorf("%s answer = %+v, want none", tc.name, got)
			}
			if svc.sent.method != "" {
				t.Errorf("service received %+v, want nothing", svc.sent)
			}
		})
	}
}

// TestEmbeddedMVCC_RestoreToRevision_BinaryRoundTrip proves an arbitrary
// byte-valued prefix/key survives the []byte<->string conversion the
// embedded transport applies between the SDK's binary-safe DTO and the
// service layer's string-keyed RestoreRevisionRequest -- a lossless,
// in-process conversion, unlike the JSON/base64 or proto `bytes` encodings
// the other transports apply across the wire.
func TestEmbeddedMVCC_RestoreToRevision_BinaryRoundTrip(t *testing.T) {
	binaryPrefix := []byte{0x00, 0xff, 0xfe, 'p'}
	binaryKey := []byte{0x00, 0xff, 0xfe, 'k'}

	svc := newRecordingMVCCService(nil)
	tr := newExtTransport(t, WithMVCCService(svc))

	result, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   binaryPrefix,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if svc.sent.restore == nil || svc.sent.restore.Prefix != string(binaryPrefix) {
		t.Fatalf("service received prefix %+v, want %v", svc.sent.restore, binaryPrefix)
	}
	if len(result.Changes) != 1 || string(result.Changes[0].Key) != "restored-key" {
		t.Fatalf("unexpected changes: %+v", result.Changes)
	}

	// Keys direction, using a genuinely non-UTF-8 key.
	if _, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Keys:     [][]byte{binaryKey},
	}); err != nil {
		t.Fatalf("RestoreToRevision (keys): %v", err)
	}
	if len(svc.sent.restore.Keys) != 1 || svc.sent.restore.Keys[0] != string(binaryKey) {
		t.Fatalf("expected service to receive binary key %v, got %v", binaryKey, svc.sent.restore.Keys)
	}
}

// ===========================================================================
// Mock: TransactionServicer
// ===========================================================================

type mockTxService struct {
	executeFn         func(ctx context.Context, compares []service.Compare, success, failure []service.Op) (*service.TransactionResponse, error)
	executeWithModeFn func(ctx context.Context, compares []service.Compare, success, failure []service.Op, mode writemode.Mode) (*service.TransactionResponse, error)
}

func (m *mockTxService) Execute(ctx context.Context, compares []service.Compare, success, failure []service.Op) (*service.TransactionResponse, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, compares, success, failure)
	}
	return &service.TransactionResponse{
		Succeeded: true,
		Revision:  1,
		Responses: []service.OpResponse{{Succeeded: true}},
	}, nil
}

func (m *mockTxService) ExecuteWithMode(ctx context.Context, compares []service.Compare, success, failure []service.Op, mode writemode.Mode) (*service.TransactionResponse, error) {
	if m.executeWithModeFn != nil {
		return m.executeWithModeFn(ctx, compares, success, failure, mode)
	}
	return &service.TransactionResponse{
		Succeeded: true,
		Revision:  1,
		Responses: []service.OpResponse{{Succeeded: true}},
	}, nil
}

func (m *mockTxService) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	return nil, errors.New("not implemented")
}

var _ service.TransactionServicer = (*mockTxService)(nil)

// txSent is what one transaction service call received.
type txSent struct {
	method   string
	compares []service.Compare
	success  []service.Op
	failure  []service.Op
	mode     writemode.Mode
}

// recordingTxService builds a mockTxService that writes what each call
// receives into *sent and answers with a revision and one op answer carrying
// a key-value, or with err when it is non-nil.
func recordingTxService(sent *txSent, err error) *mockTxService {
	answer := func() (*service.TransactionResponse, error) {
		if err != nil {
			return nil, err
		}
		return &service.TransactionResponse{Succeeded: true, Revision: 9, Responses: []service.OpResponse{{
			Succeeded: true, Count: 1,
			Kvs: []*kv.KeyValue{{Key: []byte("k"), Value: []byte("v"), CreateRevision: 2, ModRevision: 3, Version: 4, LeaseID: 5}},
		}}}, nil
	}
	return &mockTxService{
		executeFn: func(_ context.Context, compares []service.Compare, success, failure []service.Op) (*service.TransactionResponse, error) {
			*sent = txSent{method: "Execute", compares: compares, success: success, failure: failure}
			return answer()
		},
		executeWithModeFn: func(_ context.Context, compares []service.Compare, success, failure []service.Op, mode writemode.Mode) (*service.TransactionResponse, error) {
			*sent = txSent{method: "ExecuteWithMode", compares: compares, success: success, failure: failure, mode: mode}
			return answer()
		},
	}
}

// txCases returns each transaction path with the request every one of them
// sends: one compare per target and result, and success and failure ops of
// every type.
func txCases() (cases []mvccCall, wantSent txSent, want *transport.TransactionResponse) {
	req := &transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte("k1"), Target: transport.CompareValue, Result: transport.CompareEqual, Value: []byte("v")},
			{Key: []byte("k2"), Target: transport.CompareVersion, Result: transport.CompareGreater, Version: 1},
			{Key: []byte("k3"), Target: transport.CompareCreateRevision, Result: transport.CompareLess, CreateRevision: 5},
			{Key: []byte("k4"), Target: transport.CompareModRevision, Result: transport.CompareNotEqual, ModRevision: 7},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v"), LeaseID: 1},
			{Type: transport.OpGet, Key: []byte("g")},
		},
		FailureOps: []transport.Op{
			{Type: transport.OpDelete, Key: []byte("d")},
			{Type: transport.OpRange, Key: []byte("a"), RangeEnd: []byte("z")},
		},
	}
	wantSent = txSent{
		compares: []service.Compare{
			{Key: []byte("k1"), Target: service.CompareValue, Result: service.CompareEqual, Value: []byte("v")},
			{Key: []byte("k2"), Target: service.CompareVersion, Result: service.CompareGreater, Version: 1},
			{Key: []byte("k3"), Target: service.CompareCreateRevision, Result: service.CompareLess, CreateRevision: 5},
			{Key: []byte("k4"), Target: service.CompareModRevision, Result: service.CompareNotEqual, ModRevision: 7},
		},
		success: []service.Op{
			{Type: service.OpPut, Key: []byte("k"), Value: []byte("v"), LeaseID: 1},
			{Type: service.OpGet, Key: []byte("g")},
		},
		failure: []service.Op{
			{Type: service.OpDelete, Key: []byte("d")},
			{Type: service.OpRange, Key: []byte("a"), RangeEnd: []byte("z")},
		},
	}
	want = &transport.TransactionResponse{Succeeded: true, Revision: 9, Responses: []transport.OpResponse{{
		Succeeded: true, Count: 1,
		Kvs: []*transport.MVCCKeyValue{{Key: []byte("k"), Value: []byte("v"), CreateRevision: 2, ModRevision: 3, Version: 4, LeaseID: 5}},
	}}}
	cases = []mvccCall{
		{"Execute", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.ExecuteTransaction(ctx, req)
		}},
		{"ExecuteWithMode", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.ExecuteTransaction(ctx, req, transport.WithWriteMode(transport.WriteModeBatched))
		}},
		{"Execute/Request", func(ctx context.Context, tr *Transport) (any, error) {
			return requestAnswer[transport.TransactionResponse](ctx, tr, "ExecuteTransaction", req)
		}},
	}
	return cases, wantSent, want
}

// TestEmbeddedTx_SendsTheDocumentedRequest checks what each transaction path
// hands the service, compares and ops field for field, and the answer it
// decodes from the service's.
func TestEmbeddedTx_SendsTheDocumentedRequest(t *testing.T) {
	cases, base, want := txCases()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantSent := base
			wantSent.method = "Execute"
			if tc.name == "ExecuteWithMode" {
				wantSent.method, wantSent.mode = "ExecuteWithMode", writemode.Batched
			}
			var sent txSent
			got, err := tc.call(context.Background(), newExtTransport(t, WithTransactionService(recordingTxService(&sent, nil))))
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if !reflect.DeepEqual(sent, wantSent) {
				t.Errorf("service received %+v, want %+v", sent, wantSent)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("answer %+v, want %+v", got, want)
			}
		})
	}
}

// TestEmbeddedTx_SurfacesAFailedAnswer checks that a service failure on every
// transaction path comes back as that failure, with no answer.
func TestEmbeddedTx_SurfacesAFailedAnswer(t *testing.T) {
	cases, _, _ := txCases()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sent txSent
			got, err := tc.call(context.Background(), newExtTransport(t, WithTransactionService(recordingTxService(&sent, errMVCCRefused))))
			if !errors.Is(err, errMVCCRefused) {
				t.Fatalf("%s error = %v, want %v", tc.name, err, errMVCCRefused)
			}
			if !noAnswer(got) {
				t.Errorf("%s answer = %+v, want none", tc.name, got)
			}
		})
	}
}

// The transaction's MVCC revision must survive the hop through this transport,
// on BOTH the plain and the write-mode path.
//
// It did not: neither response construction copied Revision, so every
// transaction through the embedded transport reported 0 while the service
// underneath had returned the real revision. The existing tests above did not
// catch it because they assert only Succeeded — and the mock has been returning
// Revision: 1 the whole time, which is what makes this a one-line assertion
// nobody had written rather than a missing fixture.
//
// Found by review of go-qrdb#44, whose GuardedSwapResult.Revision is the first
// caller to depend on it.
func TestEmbeddedTx_PropagatesTheRevision(t *testing.T) {
	const wantRevision int64 = 4242

	svc := &mockTxService{
		executeFn: func(context.Context, []service.Compare, []service.Op, []service.Op) (*service.TransactionResponse, error) {
			return &service.TransactionResponse{
				Succeeded: true,
				Revision:  wantRevision,
				Responses: []service.OpResponse{{Succeeded: true}},
			}, nil
		},
		executeWithModeFn: func(context.Context, []service.Compare, []service.Op, []service.Op, writemode.Mode) (*service.TransactionResponse, error) {
			return &service.TransactionResponse{
				Succeeded: true,
				Revision:  wantRevision,
				Responses: []service.OpResponse{{Succeeded: true}},
			}, nil
		},
	}
	tr := newExtTransport(t, WithTransactionService(svc))
	req := &transport.TransactionRequest{
		SuccessOps: []transport.Op{{Key: []byte("k"), Value: []byte("v")}},
	}

	t.Run("plain path", func(t *testing.T) {
		resp, err := tr.ExecuteTransaction(context.Background(), req)
		if err != nil {
			t.Fatalf("ExecuteTransaction: %v", err)
		}
		if resp.Revision != wantRevision {
			t.Errorf("Revision = %d, want %d — the transport dropped it", resp.Revision, wantRevision)
		}
	})

	t.Run("write-mode path", func(t *testing.T) {
		resp, err := tr.ExecuteTransaction(context.Background(), req, transport.WithWriteMode(transport.WriteModeSync))
		if err != nil {
			t.Fatalf("ExecuteTransaction: %v", err)
		}
		if resp.Revision != wantRevision {
			t.Errorf("Revision = %d, want %d — the write-mode path dropped it", resp.Revision, wantRevision)
		}
	})
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
//
// A canned answer is right HERE and wrong elsewhere. These doubles stand in for
// the SERVICE beneath the embedded transport, and what the transport is tested
// for is forwarding: that a call reaches the service and its answer comes back.
// The swap SEMANTICS are covered where they live, against the real evaluator,
// in pkg/service and pkg/server — which is why the kvstore-level double there
// was implemented for real instead of stubbed.
func (*mockMVCCService) CompareAndSwap(_ context.Context, _ string, _, _ []byte) (bool, error) {
	return false, nil
}
