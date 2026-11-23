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

package main

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// wailsClientPath is the TypeScript Wails client the desktop frontend uses
// to reach the Go bindings through window.go.main.<Name>Binding.
const wailsClientPath = "../../sdk/typescript/src/wails.ts"

// wailsMockPath is the Wails double the mocked Playwright specs run against.
const wailsMockPath = "frontend/e2e/fixtures/wails-mock.ts"

// wailsBindingTypes maps every binding the desktop app binds (the manifest
// in gui_desktop_bindings_test.go) to its Go type. A binding that wails.ts
// reaches but this table lacks fails TestWailsBindingParity_GoMethods, so
// the table cannot drift silently from the client.
var wailsBindingTypes = map[string]reflect.Type{
	"ConnectionBinding":        reflect.TypeOf((*ConnectionBinding)(nil)),
	"ConnectionManagerBinding": reflect.TypeOf((*ConnectionManagerBinding)(nil)),
	"EmbeddedServerBinding":    reflect.TypeOf((*EmbeddedServerBinding)(nil)),
	"KVBinding":                reflect.TypeOf((*KVBinding)(nil)),
	"AdminBinding":             reflect.TypeOf((*AdminBinding)(nil)),
	"MVCCBinding":              reflect.TypeOf((*MVCCBinding)(nil)),
	"TransactionBinding":       reflect.TypeOf((*TransactionBinding)(nil)),
	"BatchBinding":             reflect.TypeOf((*BatchBinding)(nil)),
	"WatchBinding":             reflect.TypeOf((*WatchBinding)(nil)),
	"LeaseBinding":             reflect.TypeOf((*LeaseBinding)(nil)),
	"IndexBinding":             reflect.TypeOf((*IndexBinding)(nil)),
	"TimeSeriesBinding":        reflect.TypeOf((*TimeSeriesBinding)(nil)),
	"VectorBinding":            reflect.TypeOf((*VectorBinding)(nil)),
	"UserBinding":              reflect.TypeOf((*UserBinding)(nil)),
	"InvitationBinding":        reflect.TypeOf((*InvitationBinding)(nil)),
	"RoleBinding":              reflect.TypeOf((*RoleBinding)(nil)),
	"CertBinding":              reflect.TypeOf((*CertBinding)(nil)),
	"DatabaseBinding":          reflect.TypeOf((*DatabaseBinding)(nil)),
	"CollectionBinding":        reflect.TypeOf((*CollectionBinding)(nil)),
	"SQLBinding":               reflect.TypeOf((*SQLBinding)(nil)),
	"SealBinding":              reflect.TypeOf((*SealBinding)(nil)),
	"ConfigBinding":            reflect.TypeOf((*ConfigBinding)(nil)),
	"TenantBinding":            reflect.TypeOf((*TenantBinding)(nil)),
	"BackupBinding":            reflect.TypeOf((*BackupBinding)(nil)),
	"OfficerBinding":           reflect.TypeOf((*OfficerBinding)(nil)),
	"CeremonyBinding":          reflect.TypeOf((*CeremonyBinding)(nil)),
	"RebalanceBinding":         reflect.TypeOf((*RebalanceBinding)(nil)),
	"LockBinding":              reflect.TypeOf((*LockBinding)(nil)),
	"CredentialBinding":        reflect.TypeOf((*CredentialBinding)(nil)),
	"AuditBinding":             reflect.TypeOf((*AuditBinding)(nil)),
	"VolumePoolBinding":        reflect.TypeOf((*VolumePoolBinding)(nil)),
	"VlogBinding":              reflect.TypeOf((*VlogBinding)(nil)),
	"DaemonBinding":            reflect.TypeOf((*DaemonBinding)(nil)),
}

var (
	wailsBindingRefRE  = regexp.MustCompile(`getBindings\(\)\.main\.([A-Za-z]+Binding)\b`)
	wailsMethodCallRE  = regexp.MustCompile(`\bb\.([A-Z]\w*)\(`)
	wailsMockBindingRE = regexp.MustCompile(`(?m)^        ([A-Za-z]+Binding):`)
	wailsMockObjEndRE  = regexp.MustCompile(`(?m)^    };?$`)
)

// wailsClientCalls reads wails.ts and returns, per binding name, the set of
// methods the client calls on it. The text is split at each
// getBindings().main.<Name>Binding occurrence; every b.<Method>( in the
// block that follows belongs to that binding.
func wailsClientCalls(t *testing.T) map[string]map[string]struct{} {
	t.Helper()

	src, err := os.ReadFile(wailsClientPath)
	if err != nil {
		t.Fatalf("read %s: %v", wailsClientPath, err)
	}
	text := string(src)

	locs := wailsBindingRefRE.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		t.Fatalf("%s: no getBindings().main.<Name>Binding reference found", wailsClientPath)
	}

	calls := make(map[string]map[string]struct{})
	for i, loc := range locs {
		name := text[loc[2]:loc[3]]
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		block := text[loc[1]:end]
		if calls[name] == nil {
			calls[name] = make(map[string]struct{})
		}
		for _, m := range wailsMethodCallRE.FindAllStringSubmatch(block, -1) {
			calls[name][m[1]] = struct{}{}
		}
	}
	return calls
}

// sortedWailsPairs flattens a binding->methods map into sorted
// "Binding.Method" strings for stable failure messages.
func sortedWailsPairs(calls map[string]map[string]struct{}) []string {
	var pairs []string
	for binding, methods := range calls {
		for m := range methods {
			pairs = append(pairs, binding+"."+m)
		}
	}
	sort.Strings(pairs)
	return pairs
}

// TestWailsBindingParity_GoMethods asserts that every method the TypeScript
// Wails client calls on window.go.main.<Name>Binding is an exported method
// of the Go binding type. A missing method makes the Wails proxy return
// undefined, so the desktop view throws while REST-mode specs still pass.
// VectorBinding and TimeSeriesBinding calls carry a prepended database
// argument (withDatabaseArg), so presence is asserted, not arity.
func TestWailsBindingParity_GoMethods(t *testing.T) {
	t.Parallel()

	calls := wailsClientCalls(t)

	var missing []string
	for _, pair := range sortedWailsPairs(calls) {
		binding, method, _ := strings.Cut(pair, ".")
		typ, ok := wailsBindingTypes[binding]
		if !ok {
			missing = append(missing, binding+" (not in wailsBindingTypes)")
			continue
		}
		if _, ok := typ.MethodByName(method); !ok {
			missing = append(missing, pair)
		}
	}
	missing = dedupeSorted(missing)
	if len(missing) > 0 {
		t.Errorf("%s calls Go binding methods that do not exist:\n  %s",
			wailsClientPath, strings.Join(missing, "\n  "))
	}
}

// TestWailsBindingParity_Mock asserts that every (binding, method) pair the
// TypeScript Wails client calls is served by the Playwright Wails mock, so
// a mocked spec cannot pass against a method the mock lacks or invents.
func TestWailsBindingParity_Mock(t *testing.T) {
	t.Parallel()

	calls := wailsClientCalls(t)

	src, err := os.ReadFile(wailsMockPath)
	if err != nil {
		t.Fatalf("read %s: %v", wailsMockPath, err)
	}
	text := string(src)

	heads := wailsMockBindingRE.FindAllStringSubmatchIndex(text, -1)
	if len(heads) == 0 {
		t.Fatalf("%s: no `        <Name>Binding:` block found", wailsMockPath)
	}
	blocks := make(map[string]string, len(heads))
	for i, loc := range heads {
		name := text[loc[2]:loc[3]]
		var end int
		if i+1 < len(heads) {
			end = heads[i+1][0]
		} else {
			rest := text[loc[1]:]
			end = len(text)
			if e := wailsMockObjEndRE.FindStringIndex(rest); e != nil {
				end = loc[1] + e[0]
			}
		}
		blocks[name] = text[loc[1]:end]
	}

	var missing []string
	for _, pair := range sortedWailsPairs(calls) {
		binding, method, _ := strings.Cut(pair, ".")
		block, ok := blocks[binding]
		if !ok {
			missing = append(missing, binding+" (no mock block)")
			continue
		}
		keyRE := regexp.MustCompile(`\b` + regexp.QuoteMeta(method) + `\s*[:(]`)
		if !keyRE.MatchString(block) {
			missing = append(missing, pair)
		}
	}
	missing = dedupeSorted(missing)
	if len(missing) > 0 {
		t.Errorf("%s lacks methods %s calls:\n  %s",
			wailsMockPath, wailsClientPath, strings.Join(missing, "\n  "))
	}
}

// dedupeSorted removes adjacent duplicates from a sorted slice.
func dedupeSorted(in []string) []string {
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}
