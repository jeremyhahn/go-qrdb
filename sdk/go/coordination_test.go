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

package sdk

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Fakes
//
// The coordination surface is a thin wiring layer over four transport
// sub-clients. These fakes implement only what the wiring calls and record the
// arguments the wiring passes, so the unit tests below can assert the exact
// request each verb constructs without a running node. The standalone
// end-to-end test in coordination_standalone_test.go and the raft test in
// coordination_raft_test.go exercise the same surface against real services.
// ---------------------------------------------------------------------------

type fakeKV struct {
	getVal []byte
	getErr error
	putErr error

	gotPutKey string
	gotPutVal []byte
}

func (f *fakeKV) Put(_ context.Context, key string, value []byte, _ ...transport.WriteOption) error {
	f.gotPutKey, f.gotPutVal = key, value
	return f.putErr
}
func (f *fakeKV) Get(_ context.Context, _ string, _ ...transport.ReadOption) ([]byte, error) {
	return f.getVal, f.getErr
}
func (f *fakeKV) Delete(_ context.Context, _ string, _ ...transport.WriteOption) error { return nil }
func (f *fakeKV) List(_ context.Context, _ string, _ ...transport.ReadOption) ([]string, error) {
	return nil, nil
}
func (f *fakeKV) Scan(_ context.Context, _ string, _ ...transport.ReadOption) (map[string][]byte, error) {
	return nil, nil
}
func (f *fakeKV) Exists(_ context.Context, _ string, _ ...transport.ReadOption) (bool, error) {
	return false, nil
}

type fakeTxn struct {
	resp *transport.TransactionResponse
	err  error

	gotReq *transport.TransactionRequest
}

func (f *fakeTxn) ExecuteTransaction(_ context.Context, req *transport.TransactionRequest, _ ...transport.WriteOption) (*transport.TransactionResponse, error) {
	f.gotReq = req
	return f.resp, f.err
}

// succeedingTxn is a fakeTxn that reports every transaction succeeded — the
// default an AcquireLease test wants for the exclusive-bind step.
func succeedingTxn() *fakeTxn {
	return &fakeTxn{resp: &transport.TransactionResponse{Succeeded: true}}
}

type fakeWatch struct {
	ch  chan transport.WatchResponse
	err error

	gotPrefix string
}

func (f *fakeWatch) Watch(_ context.Context, _ string, _ transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	return f.ch, f.err
}
func (f *fakeWatch) WatchPrefix(_ context.Context, prefix string, _ transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	f.gotPrefix = prefix
	return f.ch, f.err
}

type fakeLease struct {
	mu sync.Mutex

	grantErr    error
	grantTTL    int64 // recorded ttl passed to LeaseGrant
	grantedID   int64
	keepAliveN  int
	keepAliveEr error

	// keepAliveBlock, when non-nil, holds LeaseKeepAlive until it is closed,
	// modelling a reply still in flight for a keepalive the server may already
	// have accepted.
	keepAliveBlock chan struct{}
	revokeErr      error
	revokeN        int
	revokedID      int64

	// revokeErrs is consumed one entry per revoke before revokeErr takes over,
	// so a test can script a backend that fails a revoke and then succeeds.
	revokeErrs []error

	// revokeBlocks makes LeaseRevoke hang until the context it was handed is
	// done, modelling a lease service that has stopped answering.
	revokeBlocks bool

	// revokeDeadline records whether the last revoke's context carried one.
	revokeDeadline bool

	// ttlErr / ttlRemaining drive LeaseTimeToLive for the Done() monitor.
	ttlErr       error
	ttlRemaining int64

	// ttlSeq scripts consecutive LeaseTimeToLive replies before the ttlErr /
	// ttlRemaining fallback takes over, so a test can model the server's
	// truncated zero followed by the honest positive reading. ttlN counts the
	// lookups the monitor actually made.
	ttlSeq []leaseTTLReply
	ttlN   int

	// ttlBlock, when non-nil, holds LeaseTimeToLive until it is closed or the
	// context it was given runs out — a lookup against a server that has gone
	// quiet.
	ttlBlock chan struct{}
}

// leaseTTLReply is one scripted LeaseTimeToLive outcome.
type leaseTTLReply struct {
	remaining int64
	err       error
}

func (f *fakeLease) LeaseGrant(_ context.Context, ttl int64) (*transport.LeaseInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grantTTL = ttl
	if f.grantErr != nil {
		return nil, f.grantErr
	}
	return &transport.LeaseInfo{ID: f.grantedID, TTL: ttl, GrantedTTL: ttl, RemainingTTL: ttl}, nil
}
func (f *fakeLease) LeaseRevoke(ctx context.Context, leaseID int64) error {
	f.mu.Lock()
	f.revokeN++
	f.revokedID = leaseID
	_, f.revokeDeadline = ctx.Deadline()
	blocks := f.revokeBlocks
	var scripted error
	if len(f.revokeErrs) > 0 {
		scripted, f.revokeErrs = f.revokeErrs[0], f.revokeErrs[1:]
	} else {
		scripted = f.revokeErr
	}
	f.mu.Unlock()

	if blocks {
		// An unresponsive lease service: the call returns only when the
		// context it was given runs out. Blocking here must NOT hold f.mu,
		// which every other fake call needs.
		<-ctx.Done()
		return ctx.Err()
	}
	return scripted
}
func (f *fakeLease) LeaseKeepAlive(ctx context.Context, leaseID int64) (*transport.LeaseInfo, error) {
	f.mu.Lock()
	f.keepAliveN++
	block := f.keepAliveBlock
	failure := f.keepAliveEr
	f.mu.Unlock()

	if block != nil {
		// A keepalive the server may already have accepted, whose REPLY is
		// still in flight. Blocking must not hold f.mu — the monitor's liveness
		// lookups need it.
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if failure != nil {
		return nil, failure
	}
	return &transport.LeaseInfo{ID: leaseID}, nil
}
func (f *fakeLease) LeaseTimeToLive(ctx context.Context, leaseID int64) (*transport.LeaseInfo, error) {
	f.mu.Lock()
	f.ttlN++
	block := f.ttlBlock
	f.mu.Unlock()

	if block != nil {
		// Blocking must not hold f.mu — a concurrent renewal needs it.
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ttlSeq) > 0 {
		reply := f.ttlSeq[0]
		f.ttlSeq = f.ttlSeq[1:]
		if reply.err != nil {
			return nil, reply.err
		}
		return &transport.LeaseInfo{ID: leaseID, RemainingTTL: reply.remaining}, nil
	}
	if f.ttlErr != nil {
		return nil, f.ttlErr
	}
	return &transport.LeaseInfo{ID: leaseID, RemainingTTL: f.ttlRemaining}, nil
}

// revokeHadDeadline reports whether the last revoke's context was bounded.
func (f *fakeLease) revokeHadDeadline() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revokeDeadline
}

// keepAlives returns how many renewals have reached the transport.
func (f *fakeLease) keepAlives() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keepAliveN
}

// lookups returns how many liveness lookups the monitor has made.
func (f *fakeLease) lookups() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ttlN
}
func (f *fakeLease) LeaseList(_ context.Context) ([]*transport.LeaseInfo, error) { return nil, nil }

// newCoordinator builds a Coordinator over the supplied fakes, failing the test
// on the (unexpected) construction error.
func newCoordinator(t *testing.T, kv *fakeKV, lease *fakeLease, txn *fakeTxn, watch *fakeWatch) *Coordinator {
	t.Helper()
	c, err := NewCoordinator(kv, lease, txn, watch)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	return c
}

// ---------------------------------------------------------------------------
// Get / Put
// ---------------------------------------------------------------------------

func TestCoordinator_Get(t *testing.T) {
	t.Run("present returns value", func(t *testing.T) {
		kv := &fakeKV{getVal: []byte("v")}
		c := newCoordinator(t, kv, &fakeLease{}, &fakeTxn{}, &fakeWatch{})
		got, err := c.Get(context.Background(), "k")
		if err != nil || string(got) != "v" {
			t.Fatalf("got (%q, %v), want (\"v\", nil)", got, err)
		}
	})

	t.Run("absent returns nil,nil", func(t *testing.T) {
		kv := &fakeKV{getErr: &transport.KeyNotFoundError{Key: "k"}}
		c := newCoordinator(t, kv, &fakeLease{}, &fakeTxn{}, &fakeWatch{})
		got, err := c.Get(context.Background(), "k")
		if err != nil || got != nil {
			t.Fatalf("got (%v, %v), want (nil, nil)", got, err)
		}
	})

	t.Run("other error propagates", func(t *testing.T) {
		sentinel := errors.New("boom")
		kv := &fakeKV{getErr: sentinel}
		c := newCoordinator(t, kv, &fakeLease{}, &fakeTxn{}, &fakeWatch{})
		_, err := c.Get(context.Background(), "k")
		if !errors.Is(err, sentinel) {
			t.Fatalf("got %v, want sentinel", err)
		}
	})
}

func TestCoordinator_Put(t *testing.T) {
	kv := &fakeKV{}
	c := newCoordinator(t, kv, &fakeLease{}, &fakeTxn{}, &fakeWatch{})
	if err := c.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if kv.gotPutKey != "k" || string(kv.gotPutVal) != "v" {
		t.Fatalf("Put delegated (%q,%q), want (k,v)", kv.gotPutKey, kv.gotPutVal)
	}
}

// ---------------------------------------------------------------------------
// CompareAndSwap
// ---------------------------------------------------------------------------

func TestCoordinator_CompareAndSwap_CreateIfAbsent(t *testing.T) {
	txn := succeedingTxn()
	c := newCoordinator(t, &fakeKV{}, &fakeLease{}, txn, &fakeWatch{})

	ok, err := c.CompareAndSwap(context.Background(), "k", nil, []byte("v"))
	if err != nil || !ok {
		t.Fatalf("got (%v,%v), want (true,nil)", ok, err)
	}
	if len(txn.gotReq.Compares) != 1 {
		t.Fatalf("nil-expect must use exactly one compare, got %d", len(txn.gotReq.Compares))
	}
	cmp := txn.gotReq.Compares[0]
	if cmp.Target != transport.CompareCreateRevision || cmp.Result != transport.CompareEqual || cmp.CreateRevision != 0 {
		t.Fatalf("nil-expect must compare create-revision==0, got %+v", cmp)
	}
	if len(txn.gotReq.SuccessOps) != 1 || txn.gotReq.SuccessOps[0].Type != transport.OpPut ||
		string(txn.gotReq.SuccessOps[0].Value) != "v" {
		t.Fatalf("success op must put the set value, got %+v", txn.gotReq.SuccessOps)
	}
}

func TestCoordinator_CompareAndSwap_ValueMatchRequiresExistence(t *testing.T) {
	txn := succeedingTxn()
	c := newCoordinator(t, &fakeKV{}, &fakeLease{}, txn, &fakeWatch{})

	ok, err := c.CompareAndSwap(context.Background(), "k", []byte("old"), []byte("new"))
	if err != nil || !ok {
		t.Fatalf("got (%v,%v), want (true,nil)", ok, err)
	}
	// The value path must pair an existence compare with the value compare, so
	// an empty expect cannot match an absent key (create-revision > 0 fails on
	// an absent key regardless of the value comparison).
	if len(txn.gotReq.Compares) != 2 {
		t.Fatalf("value path must use two compares (existence + value), got %d", len(txn.gotReq.Compares))
	}
	var haveExistence, haveValue bool
	for _, cmp := range txn.gotReq.Compares {
		switch cmp.Target {
		case transport.CompareCreateRevision:
			if cmp.Result == transport.CompareGreater && cmp.CreateRevision == 0 {
				haveExistence = true
			}
		case transport.CompareValue:
			if cmp.Result == transport.CompareEqual && string(cmp.Value) == "old" {
				haveValue = true
			}
		}
	}
	if !haveExistence || !haveValue {
		t.Fatalf("value path must compare existence AND value, got %+v", txn.gotReq.Compares)
	}
}

func TestCoordinator_CompareAndSwap_EmptyExpectStillRequiresExistence(t *testing.T) {
	txn := succeedingTxn()
	c := newCoordinator(t, &fakeKV{}, &fakeLease{}, txn, &fakeWatch{})

	// A non-nil empty expect is still the value path — it must carry the
	// existence compare, not collapse to the create path.
	if _, err := c.CompareAndSwap(context.Background(), "k", []byte{}, []byte("new")); err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if len(txn.gotReq.Compares) != 2 {
		t.Fatalf("empty expect must still use existence + value compares, got %d", len(txn.gotReq.Compares))
	}
}

func TestCoordinator_CompareAndSwap_PreconditionMiss(t *testing.T) {
	txn := &fakeTxn{resp: &transport.TransactionResponse{Succeeded: false}}
	c := newCoordinator(t, &fakeKV{}, &fakeLease{}, txn, &fakeWatch{})

	ok, err := c.CompareAndSwap(context.Background(), "k", []byte("wrong"), []byte("new"))
	if err != nil {
		t.Fatalf("precondition miss must not error, got %v", err)
	}
	if ok {
		t.Fatal("precondition miss must return ok=false")
	}
}

func TestCoordinator_CompareAndSwap_ErrorPropagates(t *testing.T) {
	sentinel := errors.New("txn down")
	txn := &fakeTxn{err: sentinel}
	c := newCoordinator(t, &fakeKV{}, &fakeLease{}, txn, &fakeWatch{})
	ok, err := c.CompareAndSwap(context.Background(), "k", nil, []byte("v"))
	if ok || !errors.Is(err, sentinel) {
		t.Fatalf("got (%v,%v), want (false, sentinel)", ok, err)
	}
}

// ---------------------------------------------------------------------------
// Watch
// ---------------------------------------------------------------------------

func TestCoordinator_Watch_FlattensAndTranslates(t *testing.T) {
	ch := make(chan transport.WatchResponse, 1)
	fw := &fakeWatch{ch: ch}
	c := newCoordinator(t, &fakeKV{}, &fakeLease{}, &fakeTxn{}, fw)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := c.Watch(ctx, "p/")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if fw.gotPrefix != "p/" {
		t.Fatalf("WatchPrefix got prefix %q, want p/", fw.gotPrefix)
	}

	ch <- transport.WatchResponse{
		Revision: 7,
		Events: []*transport.WatchEvent{
			{Type: transport.EventPut, Kv: &transport.MVCCKeyValue{Key: []byte("p/a"), Value: []byte("1"), ModRevision: 7}},
			{Type: transport.EventDelete, Kv: &transport.MVCCKeyValue{Key: []byte("p/b"), ModRevision: 8},
				PrevKv: &transport.MVCCKeyValue{Key: []byte("p/b"), Value: []byte("old")}},
		},
	}

	put := recvEvent(t, w.Events())
	if put.Type != transport.EventPut || put.Key != "p/a" || string(put.Value) != "1" || put.Revision != 7 {
		t.Fatalf("put event mistranslated: %+v", put)
	}
	del := recvEvent(t, w.Events())
	if del.Type != transport.EventDelete || del.Key != "p/b" || del.Value != nil || string(del.PrevValue) != "old" {
		t.Fatalf("delete event mistranslated: %+v", del)
	}
}

// The three ways a watch ends must be distinguishable. A consumer that cannot
// tell a compaction from an orderly shutdown will resume from where it left off
// and silently skip the changes it never saw.

func TestCoordinator_Watch_CtxCancelIsReportedAsCancellation(t *testing.T) {
	ch := make(chan transport.WatchResponse)
	fw := &fakeWatch{ch: ch}
	c := newCoordinator(t, &fakeKV{}, &fakeLease{}, &fakeTxn{}, fw)

	ctx, cancel := context.WithCancel(context.Background())
	w, err := c.Watch(ctx, "p/")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	cancel()
	assertClosed(t, w.Events())

	if !errors.Is(w.Err(), context.Canceled) {
		t.Fatalf("Err()=%v, want context.Canceled", w.Err())
	}
}

func TestCoordinator_Watch_CancellationWinsOverTheStreamClosingWithIt(t *testing.T) {
	// A real transport closes the raw stream AS A CONSEQUENCE of the caller
	// cancelling, so both arms of the drain loop's select go ready at once and
	// Go picks between them at random. Reading the closed channel first must
	// not turn the caller's own cancellation into a clean close — the two mean
	// different things to a consumer. Repeat, because a single pass could take
	// the lucky arm.
	for i := range 20 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ch := make(chan transport.WatchResponse)
		close(ch)

		c := newCoordinator(t, &fakeKV{}, &fakeLease{}, &fakeTxn{}, &fakeWatch{ch: ch})
		w, err := c.Watch(ctx, "p/")
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		assertClosed(t, w.Events())

		if !errors.Is(w.Err(), context.Canceled) {
			t.Fatalf("iteration %d: Err()=%v, want context.Canceled", i, w.Err())
		}
	}
}

func TestCoordinator_Watch_CleanServerCloseIsNotAnError(t *testing.T) {
	ch := make(chan transport.WatchResponse)
	fw := &fakeWatch{ch: ch}
	c := newCoordinator(t, &fakeKV{}, &fakeLease{}, &fakeTxn{}, fw)

	w, err := c.Watch(context.Background(), "p/")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	// The transport ends the stream without a terminal cancellation.
	close(ch)
	assertClosed(t, w.Events())

	if w.Err() != nil {
		t.Fatalf("Err()=%v, want nil for a clean close", w.Err())
	}
}

func TestCoordinator_Watch_TerminalErrorsAreTypedAndDistinct(t *testing.T) {
	// The reasons are the server's own sentinel text, which is what the
	// transport classifies on; a paraphrase would not classify.
	cases := []struct {
		name   string
		reason string
		assert func(*testing.T, error)
	}{
		{
			name:   "compaction",
			reason: "required start revision has been compacted",
			assert: func(t *testing.T, err error) {
				var compacted *transport.WatchCompactedError
				if !errors.As(err, &compacted) {
					t.Fatalf("Err()=%v, want WatchCompactedError", err)
				}
			},
		},
		{
			name:   "start revision replay unsupported",
			reason: "start revision replay is not supported by this store",
			assert: func(t *testing.T, err error) {
				var unsupported *transport.WatchStartRevisionUnsupportedError
				if !errors.As(err, &unsupported) {
					t.Fatalf("Err()=%v, want WatchStartRevisionUnsupportedError", err)
				}
			},
		},
		{
			name:   "generic server cancellation",
			reason: "leader changed",
			assert: func(t *testing.T, err error) {
				var canceled *transport.WatchCanceledError
				if !errors.As(err, &canceled) {
					t.Fatalf("Err()=%v, want WatchCanceledError", err)
				}
				// A generic cancellation must not masquerade as one of the
				// history conditions, which call for reconciliation.
				var compacted *transport.WatchCompactedError
				if errors.As(err, &compacted) {
					t.Fatal("a generic cancellation must not classify as compaction")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan transport.WatchResponse, 1)
			fw := &fakeWatch{ch: ch}
			c := newCoordinator(t, &fakeKV{}, &fakeLease{}, &fakeTxn{}, fw)

			w, err := c.Watch(context.Background(), "p/")
			if err != nil {
				t.Fatalf("Watch: %v", err)
			}
			ch <- transport.WatchResponse{Canceled: true, CancelReason: tc.reason}
			assertClosed(t, w.Events())

			got := w.Err()
			if got == nil {
				t.Fatal("a server-cancelled watch must report why it ended, not look like a clean close")
			}
			// It must also not be mistaken for the caller's own cancellation.
			if errors.Is(got, context.Canceled) {
				t.Fatalf("Err()=%v must not read as caller cancellation", got)
			}
			tc.assert(t, got)
		})
	}
}

func TestCoordinator_Watch_CallErrorPropagates(t *testing.T) {
	sentinel := errors.New("watch not configured")
	fw := &fakeWatch{err: sentinel}
	c := newCoordinator(t, &fakeKV{}, &fakeLease{}, &fakeTxn{}, fw)
	_, err := c.Watch(context.Background(), "p/")
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want sentinel", err)
	}
}

// ---------------------------------------------------------------------------
// AcquireLease / Lease
// ---------------------------------------------------------------------------

func TestCoordinator_AcquireLease_ExclusiveBindAndRoundsTTL(t *testing.T) {
	lease := &fakeLease{grantedID: 42, ttlRemaining: 100}
	txn := succeedingTxn()
	c := newCoordinator(t, &fakeKV{}, lease, txn, &fakeWatch{})

	// 1500ms rounds UP to 2s.
	l, err := c.AcquireLease(context.Background(), "owner/k", 1500*time.Millisecond)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	defer func() { _ = l.Release(context.Background()) }()

	if lease.grantTTL != 2 {
		t.Fatalf("ttl rounding: granted %ds, want 2s", lease.grantTTL)
	}
	if l.Key() != "owner/k" {
		t.Fatalf("Key()=%q, want owner/k", l.Key())
	}

	// The bind must be an exclusive create-if-absent transaction with the lease
	// attached to the put.
	if len(txn.gotReq.Compares) != 1 {
		t.Fatalf("acquire must use one create-if-absent compare, got %d", len(txn.gotReq.Compares))
	}
	cmp := txn.gotReq.Compares[0]
	if cmp.Target != transport.CompareCreateRevision || cmp.Result != transport.CompareEqual || cmp.CreateRevision != 0 {
		t.Fatalf("acquire compare must be create-revision==0, got %+v", cmp)
	}
	if len(txn.gotReq.SuccessOps) != 1 {
		t.Fatalf("acquire must have one success op, got %d", len(txn.gotReq.SuccessOps))
	}
	op := txn.gotReq.SuccessOps[0]
	if op.Type != transport.OpPut || string(op.Key) != "owner/k" || op.LeaseID != 42 {
		t.Fatalf("acquire op must put the key with the lease attached, got %+v", op)
	}
}

func TestCoordinator_AcquireLease_SubSecondFloor(t *testing.T) {
	lease := &fakeLease{grantedID: 1, ttlRemaining: 10}
	c := newCoordinator(t, &fakeKV{}, lease, succeedingTxn(), &fakeWatch{})
	l, err := c.AcquireLease(context.Background(), "k", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	defer func() { _ = l.Release(context.Background()) }()
	if lease.grantTTL != 1 {
		t.Fatalf("sub-second ttl floored to %ds, want 1s", lease.grantTTL)
	}
}

func TestCoordinator_AcquireLease_GrantErrorPropagates(t *testing.T) {
	sentinel := errors.New("lease not configured")
	lease := &fakeLease{grantErr: sentinel}
	c := newCoordinator(t, &fakeKV{}, lease, succeedingTxn(), &fakeWatch{})
	_, err := c.AcquireLease(context.Background(), "k", time.Second)
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want sentinel", err)
	}
}

func TestCoordinator_AcquireLease_AlreadyHeldRevokesGrant(t *testing.T) {
	// The exclusive-bind transaction reports the key already exists.
	lease := &fakeLease{grantedID: 9}
	txn := &fakeTxn{resp: &transport.TransactionResponse{Succeeded: false}}
	c := newCoordinator(t, &fakeKV{}, lease, txn, &fakeWatch{})

	_, err := c.AcquireLease(context.Background(), "k", time.Second)
	var held *LeaseHeldError
	if !errors.As(err, &held) || held.Key != "k" {
		t.Fatalf("got %v, want LeaseHeldError{k}", err)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.revokeN != 1 || lease.revokedID != 9 {
		t.Fatalf("a lost race must revoke the just-granted lease: revokeN=%d revokedID=%d", lease.revokeN, lease.revokedID)
	}
}

func TestCoordinator_AcquireLease_BindErrorRevokesGrant(t *testing.T) {
	lease := &fakeLease{grantedID: 9}
	txn := &fakeTxn{err: errors.New("txn failed")}
	c := newCoordinator(t, &fakeKV{}, lease, txn, &fakeWatch{})

	_, err := c.AcquireLease(context.Background(), "k", time.Second)
	if err == nil {
		t.Fatal("a transaction error must be returned")
	}
	var held *LeaseHeldError
	if errors.As(err, &held) {
		t.Fatal("a transport error must not be reported as LeaseHeldError")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.revokeN != 1 || lease.revokedID != 9 {
		t.Fatalf("a bind error must revoke the just-granted lease: revokeN=%d revokedID=%d", lease.revokeN, lease.revokedID)
	}
}

func TestCoordinator_AcquireLease_CleanupRevokeIsBounded(t *testing.T) {
	// The bind loses the key and the cleanup revoke then hangs. The cleanup is
	// detached from ctx on purpose — it must run even when ctx is already
	// cancelled — but detaching must not make it unbounded, or an unresponsive
	// lease service holds AcquireLease open forever.
	// The bound itself is what is under test, not its production length, so
	// shorten it — a test that waited out the real budget would add five idle
	// seconds to every run.
	previous := leaseCleanupTimeout
	leaseCleanupTimeout = 50 * time.Millisecond
	defer func() { leaseCleanupTimeout = previous }()

	lease := &fakeLease{grantedID: 9, revokeBlocks: true}
	txn := &fakeTxn{resp: &transport.TransactionResponse{Succeeded: false}}
	c := newCoordinator(t, &fakeKV{}, lease, txn, &fakeWatch{})

	// The caller's own context is ALREADY cancelled: the cleanup still has to
	// run, so it cannot borrow this deadline, and the bound must be its own.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		_, err := c.AcquireLease(ctx, "k", time.Second)
		done <- err
	}()

	select {
	case err := <-done:
		// The acquisition error must survive the cleanup: a failed revoke is
		// not what the caller asked about.
		var held *LeaseHeldError
		if !errors.As(err, &held) || held.Key != "k" {
			t.Fatalf("got %v, want LeaseHeldError{k}", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("AcquireLease never returned: the detached cleanup revoke is unbounded")
	}

	// The cleanup must have been given a deadline of its own — context
	// .WithoutCancel alone drops the parent's, which is what made this hang.
	if !lease.revokeHadDeadline() {
		t.Fatal("the cleanup revoke ran on a context with no deadline")
	}

	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.revokeN != 1 || lease.revokedID != 9 {
		t.Fatalf("the lost race must still attempt the revoke: revokeN=%d revokedID=%d", lease.revokeN, lease.revokedID)
	}
}

func TestLease_Renew(t *testing.T) {
	lease := &fakeLease{grantedID: 1, ttlRemaining: 100}
	c := newCoordinator(t, &fakeKV{}, lease, succeedingTxn(), &fakeWatch{})
	l, err := c.AcquireLease(context.Background(), "k", 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	defer func() { _ = l.Release(context.Background()) }()
	if err := l.Renew(context.Background()); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.keepAliveN != 1 {
		t.Fatalf("Renew must call keepalive once, got %d", lease.keepAliveN)
	}
}

func TestLease_ReleaseRevokesAndClosesDone(t *testing.T) {
	lease := &fakeLease{grantedID: 5, ttlRemaining: 100}
	c := newCoordinator(t, &fakeKV{}, lease, succeedingTxn(), &fakeWatch{})
	l, err := c.AcquireLease(context.Background(), "k", 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if err := l.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	assertClosed2(t, l.Done())

	// Release is idempotent: a second call is a no-op and revokes nothing more.
	// It must run here, NOT while holding lease.mu below — Release reaches into
	// LeaseRevoke, which takes that same lock.
	if err := l.Release(context.Background()); err != nil {
		t.Fatalf("second Release: %v", err)
	}

	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.revokeN != 1 || lease.revokedID != 5 {
		t.Fatalf("Release must revoke the lease exactly once: revokeN=%d revokedID=%d", lease.revokeN, lease.revokedID)
	}
}

// acquireLease acquires a lease over the supplied fake, failing the test on an
// unexpected acquisition error. ttl drives both the grant and the monitor's poll
// interval, so a test that wants the monitor to tick passes a short one.
func acquireLease(t *testing.T, lease *fakeLease, ttl time.Duration) Lease {
	t.Helper()
	c := newCoordinator(t, &fakeKV{}, lease, succeedingTxn(), &fakeWatch{})
	l, err := c.AcquireLease(context.Background(), "k", ttl)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	return l
}

func TestLease_FailedRevokeStaysRetryable(t *testing.T) {
	// The first revoke times out; the lease may still be held server-side, so
	// Release must report the failure rather than claim a cleanup that did not
	// happen — and must leave itself retryable.
	sentinel := errors.New("revoke timed out")
	lease := &fakeLease{grantedID: 5, ttlRemaining: 100, revokeErrs: []error{sentinel}}
	l := acquireLease(t, lease, 30*time.Second)

	if err := l.Release(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("failed revoke returned %v, want the transport error", err)
	}
	assertNotClosed(t, l.Done(), 100*time.Millisecond)

	// The retry reaches the server again and succeeds, and only now is the
	// lease released.
	if err := l.Release(context.Background()); err != nil {
		t.Fatalf("retry after a failed revoke: %v", err)
	}
	assertClosed2(t, l.Done())

	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.revokeN != 2 {
		t.Fatalf("revokeN=%d, want 2 (the failure plus the retry)", lease.revokeN)
	}
}

func TestLease_FailedRevokeLeavesMonitorRunning(t *testing.T) {
	// A failed Release must not stop the poller: the holder still holds the
	// lease, so it must still learn about an expiry it did not cause. A 3s TTL
	// puts the monitor on a 1s tick.
	lease := &fakeLease{grantedID: 6, ttlRemaining: 100, revokeErrs: []error{errors.New("revoke timed out")}}
	l := acquireLease(t, lease, 3*time.Second)

	if err := l.Release(context.Background()); err == nil {
		t.Fatal("failed revoke must return an error")
	}
	// Done still open, so what closes it below can only be the monitor — this
	// guard is what keeps the wait on Done from passing on a Release that
	// closed Done itself.
	assertNotClosed(t, l.Done(), 100*time.Millisecond)

	// Now let the lease vanish server-side. Only a live monitor can notice.
	lease.mu.Lock()
	lease.ttlErr = errors.New("lease not found")
	lease.mu.Unlock()

	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close on expiry: the failed Release stopped the monitor")
	}
}

func TestLease_ReleaseAcceptsLeaseAlreadyGone(t *testing.T) {
	// A revoke the server answers with "no such lease" is not a failure to
	// retry: the binding, and the bound key with it, is already gone.
	cases := []struct {
		name string
		err  error
	}{
		{"not found", &transport.LeaseNotFoundError{LeaseID: 8}},
		{"expired", &transport.LeaseExpiredError{LeaseID: 8}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lease := &fakeLease{grantedID: 8, ttlRemaining: 100, revokeErrs: []error{tc.err}}
			l := acquireLease(t, lease, 30*time.Second)

			if err := l.Release(context.Background()); err != nil {
				t.Fatalf("Release on an absent lease returned %v, want nil", err)
			}
			assertClosed2(t, l.Done())

			// Released means released: no second revoke.
			if err := l.Release(context.Background()); err != nil {
				t.Fatalf("second Release: %v", err)
			}
			lease.mu.Lock()
			defer lease.mu.Unlock()
			if lease.revokeN != 1 {
				t.Fatalf("revokeN=%d, want 1", lease.revokeN)
			}
		})
	}
}

func TestLease_ConcurrentRelease_FailingThenSucceedingBackend(t *testing.T) {
	// Concurrent releases serialize on the in-flight revoke. The backend fails
	// exactly one revoke, so exactly one caller may see an error; every caller
	// that returns nil must be backed by a revoke that actually succeeded.
	sentinel := errors.New("revoke timed out")
	lease := &fakeLease{grantedID: 11, ttlRemaining: 100, revokeErrs: []error{sentinel}}
	l := acquireLease(t, lease, 30*time.Second)

	const callers = 8
	errs := make([]error, callers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = l.Release(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()

	var failures int
	for i, err := range errs {
		if err == nil {
			continue
		}
		failures++
		if !errors.Is(err, sentinel) {
			t.Fatalf("caller %d got %v, want the transport error", i, err)
		}
	}
	if failures != 1 {
		t.Fatalf("%d callers saw the failed revoke, want exactly 1", failures)
	}
	assertClosed2(t, l.Done())

	lease.mu.Lock()
	defer lease.mu.Unlock()
	// Two revokes and no more: the one that failed and the one that released.
	// A higher count would mean callers kept revoking a lease already gone.
	if lease.revokeN != 2 {
		t.Fatalf("revokeN=%d, want 2 (the failure plus the release)", lease.revokeN)
	}
}

// ---------------------------------------------------------------------------
// Liveness monitoring
//
// The server truncates RemainingTTL to whole seconds and floors a negative at
// zero (pkg/lease.remainingTTL), so a reading of 0 spans everything from
// "expired" to "0.999s still to run". These tests pin down that an uncertain
// reading is confirmed before the monitor declares the lease lost, without
// letting a real loss go unreported.
// ---------------------------------------------------------------------------

func TestLease_TruncatedZeroTTLDoesNotCloseDone(t *testing.T) {
	// The first lookup catches the truncation — 0 with almost a second left —
	// and every later one reports the lease healthy. Done must stay open: the
	// holder still owns the key.
	lease := &fakeLease{
		grantedID:    3,
		ttlSeq:       []leaseTTLReply{{remaining: 0}},
		ttlRemaining: 100,
	}
	l := acquireLease(t, lease, 3*time.Second) // 1s poll interval
	defer func() { _ = l.Release(context.Background()) }()

	// Long enough for the first poll, the confirming wait, and the recheck.
	assertNotClosed(t, l.Done(), 2500*time.Millisecond)

	if n := lease.lookups(); n < 2 {
		t.Fatalf("monitor made %d lookups, want at least 2 (the zero plus a confirmation)", n)
	}
}

func TestLease_TransientTimeToLiveErrorDoesNotCloseDone(t *testing.T) {
	// A lookup that simply failed proves nothing about the lease. Only the
	// server saying the lease is absent does.
	lease := &fakeLease{
		grantedID:    4,
		ttlSeq:       []leaseTTLReply{{err: errors.New("connection reset")}},
		ttlRemaining: 100,
	}
	l := acquireLease(t, lease, 3*time.Second)
	defer func() { _ = l.Release(context.Background()) }()

	assertNotClosed(t, l.Done(), 2500*time.Millisecond)
}

func TestLease_RenewalHoldsTheLeaseThroughALookupOutage(t *testing.T) {
	// Every liveness lookup fails, but keepalive keeps succeeding — the server
	// still holds the lease and the holder is still renewing it. A run of failed
	// lookups must not manufacture a loss the server never reported.
	lease := &fakeLease{grantedID: 7, ttlErr: errors.New("connection reset")}
	l := acquireLease(t, lease, 3*time.Second) // 1s poll interval

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = l.Renew(context.Background())
			}
		}
	}()

	assertNotClosed(t, l.Done(), 5*time.Second)
}

func TestLease_LookupOutageWithoutRenewalStillClosesDone(t *testing.T) {
	// The other half of the same rule, and the reason the outage above is not
	// simply "retry until the server answers": a holder that cannot reach the
	// server cannot renew either, so its lease really is running out. Silence
	// must not postpone the signal past the lease's own lifetime — a fencing
	// signal that never arrives is what puts a second holder on the key.
	lease := &fakeLease{grantedID: 8, ttlErr: errors.New("connection reset")}
	l := acquireLease(t, lease, 3*time.Second)

	select {
	case <-l.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("an unreachable server must not keep a lapsed lease reported as held")
	}
}

// A renewal is not instantaneous, and the server accepts it before its reply
// gets home. These three pin down that window: the monitor must not overtake a
// renewal that is about to prove the lease held, must not wait on one that has
// resolved without proving it, and must not wait forever on one that never
// resolves at all.

// Ownership ends at the deadline the last ACKNOWLEDGED confirmation bought.
// These pin down both halves of that: an acknowledgement that beats the
// deadline extends it, one that misses it changes nothing and is reported to
// its caller as the stale news it is.

// blockedRenewal starts a renewal whose reply is held, and returns the lease,
// the channel that releases the reply, and the channel carrying the renewal's
// outcome. The lease has a 4s TTL — a 1s poll interval and a confirmed lifetime
// ending at roughly t0+4s — and every liveness lookup reads as uncertain, so
// the deadline, not a lookup, is what decides. The renewal goes out at t0+2s,
// halfway through the lifetime, and is still in flight when this returns.
//
// The renewal is deliberately sent at the halfway mark rather than close to the
// deadline. It has to be late enough that crediting the SEND rather than the
// acknowledgement would visibly overshoot the deadline — that is the mistake
// these tests exist to catch — while leaving a couple of seconds for a loaded
// machine to schedule the goroutine. Callers wait on Done rather than sleeping
// toward it, so this is the only wall-clock margin in play.
func blockedRenewal(t *testing.T, keepAliveErr error) (Lease, *fakeLease, chan struct{}, <-chan error) {
	t.Helper()
	release := make(chan struct{})
	lease := &fakeLease{
		grantedID:      12,
		ttlRemaining:   0, // every lookup uncertain
		keepAliveBlock: release,
		keepAliveEr:    keepAliveErr,
	}
	start := time.Now()
	l := acquireLease(t, lease, 4*time.Second)

	time.Sleep(2 * time.Second)
	outcome := make(chan error, 1)
	go func() { outcome <- l.Renew(context.Background()) }()

	// Let the keepalive reach the fake and block there, so what follows really
	// does exercise the in-flight window rather than racing past it.
	waitUntil := time.Now().Add(time.Second)
	for lease.keepAlives() == 0 {
		if time.Now().After(waitUntil) {
			t.Fatal("the keepalive never reached the transport: the in-flight window was never entered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the renewal reached the transport only after %v of a 4s lifetime, "+
			"leaving no room to reason about the deadline: this machine is too loaded "+
			"for these timings rather than the contract being broken", elapsed)
	}
	select {
	case err := <-outcome:
		t.Fatalf("Renew returned %v before its reply was released: nothing was in flight", err)
	default:
	}
	return l, lease, release, outcome
}

func TestLease_RenewalAcknowledgedBeforeTheDeadlineKeepsTheLease(t *testing.T) {
	// The reply is released at once, so the ack lands around t0+2s — halfway
	// through the confirmed lifetime — and extends the deadline.
	l, _, release, outcome := blockedRenewal(t, nil)
	close(release)

	select {
	case err := <-outcome:
		if err != nil {
			t.Fatalf("an acknowledgement inside the lifetime must renew: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Renew never returned")
	}

	// The extension runs to roughly t0+6s, well past the original t0+4s.
	assertNotClosed(t, l.Done(), time.Second)
}

func TestLease_RenewalAcknowledgedAfterTheDeadlineReportsTheLeaseLost(t *testing.T) {
	// The complement, and the case that makes the hard deadline honest. The
	// reply is held until ownership has demonstrably ended, so the ack that
	// follows is stale; saying "renewed" to it would send a holder that has
	// already fenced back to work.
	l, _, release, outcome := blockedRenewal(t, nil)

	select {
	case <-l.Done():
	case <-time.After(8 * time.Second):
		t.Fatal("an unacknowledged renewal must not hold ownership past the deadline")
	}

	close(release)
	select {
	case err := <-outcome:
		var lost *LeaseLostError
		if !errors.As(err, &lost) {
			t.Fatalf("a late acknowledgement must report the lease lost, got %v", err)
		}
		if lost.Key != "k" {
			t.Fatalf("LeaseLostError names %q, want k", lost.Key)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Renew never returned")
	}

	// Whoever fences on Done must be looking at the same answer this caller
	// just got, whichever of the two noticed first.
	select {
	case <-l.Done():
	default:
		t.Fatal("Renew reported the lease lost while Done was still open")
	}
}

func TestLease_UnacknowledgedRenewalEndsOwnershipAtTheDeadline(t *testing.T) {
	// The timing assertion behind the contract: a keepalive stalled in
	// transport — one that may never reach the server at all — cannot postpone
	// the deadline even by a poll interval. If it could, the server would
	// expire this lease and hand the key to a contender while this holder still
	// believed it owned it.
	start := time.Now()
	l, lease, release, _ := blockedRenewal(t, nil)
	defer close(release)

	select {
	case <-l.Done():
	case <-time.After(8 * time.Second):
		t.Fatal("a stalled renewal held ownership open past the deadline")
	}

	elapsed := time.Since(start)
	// The confirmed lifetime ends at t0+4s. Earlier would be premature; crediting
	// the unacknowledged send instead would reach to t0+6s.
	if elapsed < 3800*time.Millisecond {
		t.Fatalf("ownership ended after %v, before the confirmed lifetime was spent", elapsed)
	}
	if elapsed > 4800*time.Millisecond {
		t.Fatalf("ownership ended after %v, want the deadline at ~4s: an unacknowledged renewal deferred it", elapsed)
	}
	// The renewal really was outstanding across the deadline.
	if n := lease.keepAlives(); n != 1 {
		t.Fatalf("keepalives=%d, want exactly 1 outstanding across the deadline", n)
	}
}

func TestLease_FailedRenewalInFlightStillClosesDoneOnceItResolves(t *testing.T) {
	// A renewal that resolves as a failure proves nothing, so the deadline
	// governs — and under the hard deadline it has already governed by the time
	// the failure lands.
	l, _, release, outcome := blockedRenewal(t, errors.New("keepalive refused"))

	close(release)
	select {
	case err := <-outcome:
		if err == nil {
			t.Fatal("Renew must report the failure")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Renew never returned")
	}

	select {
	case <-l.Done():
	case <-time.After(8 * time.Second):
		t.Fatal("a renewal that resolved without extending the lease must not hold Done open")
	}
}

func TestLease_HungRenewalDoesNotDelayTheDeadline(t *testing.T) {
	// A renewal on a context with no deadline that never resolves at all. It
	// has no bearing on ownership: nothing unacknowledged does.
	l, _, release, _ := blockedRenewal(t, nil)
	defer close(release) // let the goroutine go at test end

	select {
	case <-l.Done():
	case <-time.After(8 * time.Second):
		t.Fatal("a renewal that never resolves must not delay the deadline")
	}
}

func TestLease_OwnershipEndsAtTheDeadlineNotTheNextPoll(t *testing.T) {
	// A poll cadence lines up with the deadline only by coincidence. A 7s TTL
	// polls every 2s — 2s, 4s, 6s, 8s — while the confirmed lifetime ends at
	// 7s, so a monitor that only ever decides on a poll boundary would hold
	// ownership a whole second past the point the server may have handed the
	// key to somebody else. Lookups here answer instantly, so nothing but the
	// cadence is in play.
	lease := &fakeLease{grantedID: 14, ttlRemaining: 0} // every lookup uncertain

	start := time.Now()
	l := acquireLease(t, lease, 7*time.Second)

	select {
	case <-l.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("ownership outlasted the confirmed lifetime")
	}

	elapsed := time.Since(start)
	if elapsed < 6800*time.Millisecond {
		t.Fatalf("ownership ended after %v, before the confirmed lifetime was spent", elapsed)
	}
	if elapsed > 7600*time.Millisecond {
		t.Fatalf("ownership ended after %v, want ~7s: it waited for the 8s poll", elapsed)
	}
	if n := lease.lookups(); n == 0 {
		t.Fatal("no lookup was ever made: the poll loop never ran")
	}
}

func TestLease_BlockedLookupCannotOutlastTheDeadline(t *testing.T) {
	// The other way a lookup can defer the signal: by running long. A 7s TTL
	// polls every 2s, so the ticks fall at 2s, 4s and 6s while the confirmed
	// lifetime ends at 7s — between two of them. A lookup that hangs must be
	// cut short at the deadline rather than running its full budget to 8s,
	// which is where ownership would otherwise end.
	block := make(chan struct{})
	defer close(block)
	lease := &fakeLease{grantedID: 13, ttlBlock: block}

	start := time.Now()
	l := acquireLease(t, lease, 7*time.Second)

	select {
	case <-l.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("a hung liveness lookup held ownership open past the deadline")
	}

	elapsed := time.Since(start)
	if elapsed < 6800*time.Millisecond {
		t.Fatalf("ownership ended after %v, before the confirmed lifetime was spent", elapsed)
	}
	if elapsed > 7600*time.Millisecond {
		t.Fatalf("ownership ended after %v, want ~7s: the lookup ran past the deadline", elapsed)
	}
	if n := lease.lookups(); n == 0 {
		t.Fatal("no lookup was ever made: the blocking-probe window was never entered")
	}
}

func TestLease_PersistentZeroTTLClosesDone(t *testing.T) {
	// The other direction: confirming an uncertain reading must not turn into
	// never reporting loss. A lease that keeps reading 0 really has expired.
	lease := &fakeLease{grantedID: 5, ttlRemaining: 0}
	l := acquireLease(t, lease, 3*time.Second)

	select {
	case <-l.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("a lease reading 0 on every lookup must still be reported lost")
	}
}

func TestLease_ServerReportedAbsenceClosesDoneWithoutConfirming(t *testing.T) {
	// A typed not-found is the server itself saying the lease is gone. That is
	// proof, so it costs no confirmation round.
	lease := &fakeLease{grantedID: 6, ttlErr: &transport.LeaseNotFoundError{LeaseID: 6}}
	l := acquireLease(t, lease, 3*time.Second)

	select {
	case <-l.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("a server-reported absent lease must close Done")
	}
	if n := lease.lookups(); n != 1 {
		t.Fatalf("monitor made %d lookups, want 1: proof needs no confirmation", n)
	}
}

func TestLease_DoneClosesOnExpiry(t *testing.T) {
	// TTL of 1s makes the monitor poll every 1s; TimeToLive reporting the lease
	// gone must close Done without a Release.
	lease := &fakeLease{grantedID: 7, ttlErr: errors.New("lease not found")}
	c := newCoordinator(t, &fakeKV{}, lease, succeedingTxn(), &fakeWatch{})
	l, err := c.AcquireLease(context.Background(), "k", time.Second)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close after the lease expired")
	}
}

// ---------------------------------------------------------------------------
// Constructor guards
// ---------------------------------------------------------------------------

func TestNewCoordinator_NilArgs(t *testing.T) {
	full := func() (*fakeKV, *fakeLease, *fakeTxn, *fakeWatch) {
		return &fakeKV{}, &fakeLease{}, &fakeTxn{}, &fakeWatch{}
	}
	cases := []struct {
		name string
		call func() (*Coordinator, error)
		want string
	}{
		{"nil kv", func() (*Coordinator, error) {
			_, l, tx, w := full()
			return NewCoordinator(nil, l, tx, w)
		}, "kv"},
		{"nil lease", func() (*Coordinator, error) {
			k, _, tx, w := full()
			return NewCoordinator(k, nil, tx, w)
		}, "lease"},
		{"nil txn", func() (*Coordinator, error) {
			k, l, _, w := full()
			return NewCoordinator(k, l, nil, w)
		}, "transaction"},
		{"nil watch", func() (*Coordinator, error) {
			k, l, tx, _ := full()
			return NewCoordinator(k, l, tx, nil)
		}, "watch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.call()
			var unsupported *CoordinationUnsupportedError
			if !errors.As(err, &unsupported) || unsupported.Capability != tc.want {
				t.Fatalf("got %v, want CoordinationUnsupportedError{%q}", err, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func recvEvent(t *testing.T, out <-chan Event) Event {
	t.Helper()
	select {
	case e, ok := <-out:
		if !ok {
			t.Fatal("watch channel closed before delivering an expected event")
		}
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a watch event")
		return Event{}
	}
}

func assertClosed(t *testing.T, out <-chan Event) {
	t.Helper()
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("expected watch channel to be closed, got a value")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch channel was not closed")
	}
}

// assertNotClosed fails if Done closes within d — the lease is still held, so
// the holder's ownership signal must stay open.
func assertNotClosed(t *testing.T, done <-chan struct{}, d time.Duration) {
	t.Helper()
	select {
	case <-done:
		t.Fatal("Done closed while the lease was still held")
	case <-time.After(d):
	}
}

func assertClosed2(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Done channel was not closed")
	}
}
