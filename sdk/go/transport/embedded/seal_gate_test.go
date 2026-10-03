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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// gateProbe is a SealProbe whose answer the test sets.
type gateProbe struct{ sealed atomic.Bool }

func (p *gateProbe) IsSealed() bool { return p.sealed.Load() }

func closedProbe() *gateProbe {
	p := &gateProbe{}
	p.sealed.Store(true)
	return p
}

var (
	errorType   = reflect.TypeFor[error]()
	contextType = reflect.TypeFor[context.Context]()
)

// minGatedMethods is a discovery floor: every assertion below sits inside a
// loop over the method set, and an empty loop reports success.
const minGatedMethods = 200

// callZero calls method m of v with a background context and zero values for
// every other argument. The gate runs before any argument is read.
func callZero(v reflect.Value, m reflect.Method) error {
	ft := m.Type
	args := []reflect.Value{v}
	for i := 1; i < ft.NumIn(); i++ {
		if in := ft.In(i); in == contextType {
			args = append(args, reflect.ValueOf(context.Background()))
		} else {
			args = append(args, reflect.Zero(in))
		}
	}
	var out []reflect.Value
	if ft.IsVariadic() {
		out = m.Func.CallSlice(args)
	} else {
		out = m.Func.Call(args)
	}
	err, _ := out[len(out)-1].Interface().(error)
	return err
}

// requestMethods returns *Transport's request methods: exported, ending in an
// error, and not a lifecycle method.
func requestMethods() []reflect.Method {
	typ := reflect.TypeFor[*Transport]()
	var out []reflect.Method
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		if ft := m.Type; ft.NumOut() == 0 || ft.Out(ft.NumOut()-1) != errorType {
			continue
		}
		if _, lifecycle := ungatedLifecycleMethods[m.Name]; lifecycle {
			continue
		}
		out = append(out, m)
	}
	return out
}

// TestSealGate_RefusesEveryRequestOutsideTheAllowlist pins Tier-1 parity for
// the node-wide seal gate: while the node barrier is sealed -- or the node is
// held, cut off or not yet confirmed by a cluster seal -- the embedded
// transport refuses every request the network transports refuse, with the
// typed sealed refusal, and only the sealed-window allowlist gets through.
// The method set is walked by reflection, so a request method added without
// the gate fails here.
func TestSealGate_RefusesEveryRequestOutsideTheAllowlist(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{}, WithSealProbe(closedProbe()))
	if err != nil {
		t.Fatal(err)
	}
	allow := sealedwindow.EmbeddedAllowlist()
	v := reflect.ValueOf(tr)
	gated := 0
	for _, m := range requestMethods() {
		if _, ok := allow[m.Name]; ok {
			continue
		}
		gated++
		var sealed *BarrierSealedError
		if err := callZero(v, m); !errors.As(err, &sealed) {
			t.Errorf("%s with the gate closed: err = %v, want *BarrierSealedError", m.Name, err)
		}
	}
	if gated < minGatedMethods {
		t.Fatalf("gated %d request methods, want at least %d -- the walk found too few", gated, minGatedMethods)
	}
}

// TestSealGate_AllowlistNamesRealMethods pins every embedded allowlist entry to
// a request method that exists, and no allowlisted method to the refusal.
func TestSealGate_AllowlistNamesRealMethods(t *testing.T) {
	typ := reflect.TypeFor[*Transport]()
	for name := range sealedwindow.EmbeddedAllowlist() {
		if _, ok := typ.MethodByName(name); !ok {
			t.Errorf("sealed-window allowlist names %q, which *Transport does not have", name)
		}
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithSealProbe(closedProbe()),
		WithBarrierService(&mockBarrierService{
			barrierClusterUnsealFn: func(context.Context) (*clusterseal.Report, error) {
				return &clusterseal.Report{Active: true, Epoch: 2}, nil
			},
			barrierStatusFn: func(context.Context) (*service.BarrierStatusInfo, error) {
				return &service.BarrierStatusInfo{Sealed: false}, nil
			},
		}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.BarrierStatus(context.Background()); err != nil {
		t.Fatalf("BarrierStatus with the gate closed: %v", err)
	}
	if _, err := tr.BarrierClusterUnseal(context.Background()); err != nil {
		t.Fatalf("BarrierClusterUnseal with the gate closed: %v", err)
	}
}

// TestSealGate_OpensWithTheProbe proves the gate follows the probe both ways,
// on the node's transport and on a database-scoped one, and that a transport
// with no probe gates nothing.
func TestSealGate_OpensWithTheProbe(t *testing.T) {
	probe := closedProbe()
	tr, err := New(newMockKVService(), &mockAdminService{}, WithSealProbe(probe))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var sealed *BarrierSealedError
	if err := tr.Put(ctx, "k", []byte("v")); !errors.As(err, &sealed) {
		t.Fatalf("Put with the gate closed: %v", err)
	}
	probe.sealed.Store(false)
	if err := tr.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put with the gate open: %v", err)
	}

	// A database-scoped transport inherits the gate.
	scopedProbe := closedProbe()
	base, err := New(newMockKVService(), &mockAdminService{}, WithServiceRegistry(newScopeRegistry()), WithSealProbe(scopedProbe))
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := base.WithDatabase("ledger")
	if err != nil {
		t.Fatal(err)
	}
	if err := scoped.(*Transport).Put(ctx, "k", []byte("v")); !errors.As(err, &sealed) {
		t.Fatalf("scoped Put with the gate closed: %v", err)
	}
	scopedProbe.sealed.Store(false)
	if err := scoped.(*Transport).Put(ctx, "k", []byte("v")); errors.As(err, &sealed) {
		t.Fatalf("scoped Put with the gate open was refused as sealed: %v", err)
	}

	open, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatal(err)
	}
	if err := open.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put with no probe: %v", err)
	}
}

// minGatedSubClientMethods is the sub-client walk's discovery floor, for the
// same reason as minGatedMethods.
const minGatedSubClientMethods = 20

// subClientAccessors returns *Transport's sub-client accessors: exported,
// no arguments, one interface result, named "...Client". They return no
// error, so requestMethods does not see them -- and the methods of what they
// return are requests like any other.
func subClientAccessors() []reflect.Method {
	typ := reflect.TypeFor[*Transport]()
	var out []reflect.Method
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		ft := m.Type
		if ft.NumIn() != 1 || ft.NumOut() != 1 || ft.Out(0).Kind() != reflect.Interface || !strings.HasSuffix(m.Name, "Client") {
			continue
		}
		out = append(out, m)
	}
	return out
}

// callZeroBound calls the bound method fn with a background context and zero
// values for every other argument, returning its trailing error.
func callZeroBound(fn reflect.Value) error {
	ft := fn.Type()
	var args []reflect.Value
	for i := 0; i < ft.NumIn(); i++ {
		if in := ft.In(i); in == contextType {
			args = append(args, reflect.ValueOf(context.Background()))
		} else {
			args = append(args, reflect.Zero(in))
		}
	}
	var out []reflect.Value
	if ft.IsVariadic() {
		out = fn.CallSlice(args)
	} else {
		out = fn.Call(args)
	}
	err, _ := out[len(out)-1].Interface().(error)
	return err
}

// TestSealGate_RefusesEverySubClientRequest extends the walk through every
// sub-client accessor (AuditClient, LoginClient, ...): while the gate is
// closed, every error-returning method of every returned client is refused
// with *BarrierSealedError unless "<Accessor>.<Method>" is allowlisted. An
// accessor that hands back the transport itself is already covered by the
// request-method walk.
func TestSealGate_RefusesEverySubClientRequest(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{}, WithSealProbe(closedProbe()),
		WithAuditLogger(audit.NewNoopLogger()))
	if err != nil {
		t.Fatal(err)
	}
	allow := sealedwindow.EmbeddedAllowlist()
	trPtr := reflect.ValueOf(tr).Pointer()
	gated := 0
	for _, acc := range subClientAccessors() {
		client := reflect.ValueOf(tr).MethodByName(acc.Name).Call(nil)[0]
		if client.IsNil() {
			t.Errorf("%s() returned nil; the test must wire what it needs", acc.Name)
			continue
		}
		dyn := client.Elem()
		if dyn.Kind() == reflect.Pointer && dyn.Pointer() == trPtr {
			continue
		}
		for i := 0; i < dyn.NumMethod(); i++ {
			m := dyn.Type().Method(i)
			if ft := m.Type; ft.NumOut() == 0 || ft.Out(ft.NumOut()-1) != errorType {
				continue
			}
			name := acc.Name + "." + m.Name
			if _, ok := allow[name]; ok {
				continue
			}
			gated++
			var sealed *BarrierSealedError
			if err := callZeroBound(dyn.Method(i)); !errors.As(err, &sealed) {
				t.Errorf("%s with the gate closed: err = %v, want *BarrierSealedError", name, err)
			}
		}
	}
	if gated < minGatedSubClientMethods {
		t.Fatalf("gated %d sub-client methods, want at least %d -- the walk found too few", gated, minGatedSubClientMethods)
	}
}
