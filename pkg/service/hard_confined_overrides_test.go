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

package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"
)

// interfaceMethodNames returns every method name an interface declares,
// discovered via reflection on a nil pointer typed to that interface (e.g.
// (*KVServicer)(nil)).
func interfaceMethodNames(t *testing.T, nilIfacePtr any) map[string]bool {
	t.Helper()
	typ := reflect.TypeOf(nilIfacePtr).Elem()
	if typ.Kind() != reflect.Interface {
		t.Fatalf("interfaceMethodNames: %v is not an interface type", typ)
	}
	names := make(map[string]bool, typ.NumMethod())
	for i := 0; i < typ.NumMethod(); i++ {
		names[typ.Method(i).Name] = true
	}
	return names
}

// declaredMethodsWithReceiver parses file and returns the set of method
// names declared with a receiver named receiverType (pointer or value
// receiver). A method promoted onto receiverType through an embedded
// field never appears here, because it has no FuncDecl of its own in this
// file -- which is exactly the property this file's tests need: a
// promoted method is invisible to this scan, so an interface method
// missing from the result means it would reach the decorator only by
// promotion.
func declaredMethodsWithReceiver(t *testing.T, file, receiverType string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	names := make(map[string]bool)
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		recvType := fn.Recv.List[0].Type
		if star, ok := recvType.(*ast.StarExpr); ok {
			if ident, ok := star.X.(*ast.Ident); ok && ident.Name == receiverType {
				names[fn.Name.Name] = true
			}
			continue
		}
		if ident, ok := recvType.(*ast.Ident); ok && ident.Name == receiverType {
			names[fn.Name.Name] = true
		}
	}
	return names
}

// TestHardConfinedDecorators_OverrideEveryInterfaceMethod guards against
// go-qrdb#555's B2b class of bug: each of these decorators embeds a bare
// SERVICER INTERFACE anonymously (not a concrete, already-confined type),
// so any method of that interface the decorator does not declare for
// itself is promoted straight through to the raw, unconfined wrapped
// value -- exactly what happened to KVServicer's four entity methods,
// TransactionServicer/BatchServicer's ExecuteEntity, BatchServicer's
// Import, and WatchServicer's two entity-watch methods before this fix.
// A newly added interface method is, by default, exactly that kind of
// promoted method, so this parses each decorator's own source file and
// fails the moment an interface gains a method the decorator's file does
// not declare a FuncDecl for.
//
// hardConfinedMVCC is the one exception worth naming: it embeds
// hardConfinedKV (a concrete, already-confined type) rather than a bare
// KVServicer, and holds its own MVCCKVServicer in a NAMED (not embedded)
// field, so none of that field's methods promote onto hardConfinedMVCC at
// all -- every MVCCKVServicer method not covered by the embedded
// hardConfinedKV must already be declared directly on hardConfinedMVCC
// itself for the type to satisfy the interface, which the compiler
// enforces independently of this test. Its case below checks the union of
// hard_confined_mvcc.go's own declarations and hardConfinedKV's (the
// KVServicer portion, verified by its own case here too).
func TestHardConfinedDecorators_OverrideEveryInterfaceMethod(t *testing.T) {
	cases := []struct {
		name  string
		iface any
		union []struct {
			file         string
			receiverType string
		}
	}{
		{
			name:  "hardConfinedKV/KVServicer",
			iface: (*KVServicer)(nil),
			union: []struct{ file, receiverType string }{
				{"hard_confined_kv.go", "hardConfinedKV"},
			},
		},
		{
			name:  "hardConfinedMVCC/MVCCKVServicer",
			iface: (*MVCCKVServicer)(nil),
			union: []struct{ file, receiverType string }{
				{"hard_confined_mvcc.go", "hardConfinedMVCC"},
				{"hard_confined_kv.go", "hardConfinedKV"},
			},
		},
		{
			name:  "hardConfinedWatch/WatchServicer",
			iface: (*WatchServicer)(nil),
			union: []struct{ file, receiverType string }{
				{"hard_confined_watch.go", "hardConfinedWatch"},
			},
		},
		{
			name:  "hardConfinedTxn/TransactionServicer",
			iface: (*TransactionServicer)(nil),
			union: []struct{ file, receiverType string }{
				{"hard_confined_txn.go", "hardConfinedTxn"},
			},
		},
		{
			name:  "hardConfinedBatch/BatchServicer",
			iface: (*BatchServicer)(nil),
			union: []struct{ file, receiverType string }{
				{"hard_confined_txn.go", "hardConfinedBatch"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := interfaceMethodNames(t, tc.iface)
			got := make(map[string]bool)
			for _, u := range tc.union {
				for name := range declaredMethodsWithReceiver(t, u.file, u.receiverType) {
					got[name] = true
				}
			}
			for name := range want {
				if !got[name] {
					t.Errorf("interface method %q is not declared directly on any of %v -- it would be promoted through an embedded interface field, bypassing confinement", name, tc.union)
				}
			}
		})
	}
}
