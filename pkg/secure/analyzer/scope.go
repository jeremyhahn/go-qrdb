// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package analyzer

import (
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/inspector"
)

// THE SCOPE OF RULE 3, AND WHY IT IS NOT THE WHOLE TREE (go-qrdb#429)
//
// Rule 3's remedy is `*secure.Secret` / `*secure.Buffer`, and that remedy is
// only available where THIS PROGRAM CHOOSES THE REPRESENTATION. Where some
// other mechanism materialises the plaintext into memory this code did not
// allocate — a decoder, a code generator, the flag parser reading os.Args —
// the field type sits DOWNSTREAM of the copy that matters, and changing it
// buys a custom codec plus a lifetime contract without removing that copy.
//
// That single sentence is the whole justification. It is spelled out three
// times below because the three detectors are independently wrong-able, not
// because there are three arguments.
//
// A fourth clause, `_test.go`, rests on a DIFFERENT argument and is kept
// visibly separate for that reason. See testFileScope.
//
// WHAT THIS IS NOT. It is not a baseline and not a per-finding exclusion
// list. Each clause is a structural property of the declaration, evaluated
// fresh on every run; none of them names a file, a package or a line. A new
// violation of the same shape is silenced by the same reasoning, and a new
// violation of any other shape is reported. The counts below are what the
// clauses matched on the tree the day they landed, recorded so the next
// reader can re-measure rather than re-argue — they are not an input to
// anything.
//
// MEASURED ON main AT ff3d477 and RE-MEASURED at 6ee9b8a after #427 landed
// (2026-09-11) — byte-identical results, 30/30/31/31 in the root module and 22
// in sdk/go. Root module + sdk/go module, swept across all four combinations
// in the Makefile's SUPPORTED_TAG_SETS:
//
//	rule 1 (raw memguard import)                     1   FIXED in this change
//	rule 2 (ssh.ParsePrivateKey)                     0
//	rule 3, generated file                           8   silenced by S1
//	rule 3, codec-tagged field                      23   silenced by S2
//	rule 3, command-line flag target                 3   silenced by S3
//	rule 3, _test.go                                 9   silenced by S4
//	rule 3, everything else                          2   FIXED in this change
//	                                                ---
//	                                                 46
//
// So 43 of 46 diagnostics are silenced and 3 are fixed, and a green gate after
// this change means "no holder of key material in this tree declares it as a
// raw type", not "the tree is clean". The honest reading of the residue is
// that rule 3 as written matched 45 CARRIERS and 0 HOLDERS: every one of the
// 45 was a DTO, a generated wire struct, a flag target or a test fixture,
// passing material through to seal.Credentials, and not one was a structure
// that keeps key material alive across requests. Those sites are already
// Buffer-backed, which is why the rule found nothing there.
//
// A rule that matches nothing on a clean tree is only worth having if it is
// demonstrably not vacuous. `make secure-analyzer-test` is that demonstration:
// it reintroduces a raw memguard import and a raw holder field and requires
// the gate to go red on both.

// THE RULE COVERS DECLARATIONS, NOT LIFETIMES — AND THIS GATE CAUGHT NOTHING
// WHEN THAT BIT (go-qrdb#429).
//
// Written down here by the lane that hit it, because it is the sort of limit
// that is obvious once stated and invisible until then.
//
// Rule 3 asks a question about a FIELD: is this long-lived declaration of key
// material the wipeable type? It asks nothing about what happens to the bytes
// afterwards. Converting two config fields to *secure.Buffer in this change
// satisfied it at both sites — and at one of them the conversion made the
// tree MEASURABLY WORSE, with the analyzer green throughout:
//
//	before   softwareSecret = config.Barrier.Passphrase
//	         an ALIAS. One plaintext copy, owned by the embedder, removed by
//	         the embedder's own wipe.
//	after    softwareSecret = append([]byte(nil), b...)   inside WithBytes
//	         the source is now mlocked, AND there is a second copy in
//	         ordinary pageable memory that no caller can reach, created on
//	         every NewServer, for the life of the process.
//
// Net: +1 unrecoverable plaintext copy, exposed to exactly the routes this
// package exists to close — a core dump, a swapped page, /proc/<pid>/mem —
// and nothing observable changed. It was caught in review, not by the gate.
//
// So what securecheck's green sweep means is narrower than it looks: NO
// DECLARATION IN THIS TREE HOLDS KEY MATERIAL AS A RAW TYPE. It does not mean
// the copies taken out of those declarations are bounded.
//
// That needed a different analysis from anything in this file: dataflow from
// every borrow to a wipe, a return, or a consumer that takes ownership. It is
// securecopy (copyout.go, go-qrdb#459), a second analyzer in the same vet
// tool, and it reports the #429 instance above. Its limits are listed there;
// what it cannot see is still a review obligation. The codec-boundary standard
// S2 states ("every handler that reads one wipes it before returning") is one
// of those limits: a decoder-materialised field never lived in protected
// memory, so securecopy has no source for it.

// generatedFileScope reports whether f carries the Go toolchain's canonical
// generated-code marker.
//
// S1 — GENERATED FILES.
//
// pkg/api/grpc/proto/qrdbv1/qrdb.pb.go declares eight sensitive-named string
// and []byte fields. protoc rewrites that file from the .proto on every
// `make proto`, so it can carry NEITHER a hand-edit NOR an inline
// suppression: both are erased by the next generation, silently, and the gate
// turns red on a commit that changed nothing. That is the same wall go-qrdb#395
// hit trying to suppress gosec G103 in generated code.
//
// Changing the .proto instead does not help either, because the field type
// there is the WIRE type — `bytes passphrase` is what the protocol carries,
// and protoc has no mapping from it to *secure.Buffer. The remedy rule 3 asks
// for does not exist at this layer in any form.
//
// ast.IsGenerated implements the convention from
// https://go.dev/s/generatedcode, so this clause is defined by the toolchain
// and not by a path list. A hand-written file cannot enter it by accident; a
// file that adds the marker to escape the rule is lying about being generated,
// which is a reviewable claim in a diff, unlike a silent exclusion entry.
func generatedFileScope(f *ast.File) bool {
	return ast.IsGenerated(f)
}

// codecTags is the set of struct-tag keys that mean "a decoder outside this
// package materialises this field".
//
// It is the codec set this repository actually builds — see the Makefile's
// BUILD_TAGS: codec_json, codec_msgpack, codec_yaml, codec_bson,
// codec_protobuf — plus xml and toml, which no build tag selects but which a
// consumer of the SDK's types may drive through encoding/xml or a config
// loader. Adding a key here widens the exemption and belongs in a change that
// says so.
var codecTags = []string{
	"json",
	"protobuf",
	"msgpack",
	"yaml",
	"bson",
	"xml",
	"toml",
}

// codecTaggedScope reports whether a struct field is at a serialization
// boundary, i.e. whether it carries a codec struct tag that names it on the
// wire.
//
// S2 — TRANSPORT / DTO FIELDS.
//
// This is the clause go-qrdb#429 was filed to settle, so the reasoning is
// written out rather than assumed.
//
// By the time a handler sees `req.Passphrase`, encoding/json has already
// allocated the plaintext: it scanned the request body, unquoted the value and
// built a string or a []byte on the Go heap. Declaring the field
// *secure.Buffer does not prevent that allocation — it adds a
// json.Unmarshaler that receives the SAME already-materialised bytes and
// copies them into mlocked memory, leaving the decoder's copy behind exactly
// as before. The field type is the wrong control at this layer; the right ones
// are a short handler and an explicit wipe, which is what the handlers do.
//
// THE EVIDENCE THAT SETTLES IT, and the reason this is not a convenience
// carve-out: sdk/go/transport/seal_store_types.go:20 and :31 are ALREADY
// []byte and are still flagged, because rule 3 bans []byte too. go-qrdb#409
// converted the barrier credential fields from string to []byte end to end and
// made every handler wipe them — a real and measurable improvement in
// wipeability — and it moved NOT ONE of these diagnostics. A rule that cannot
// be satisfied by the correct fix is mis-scoped, and it is the rule that has
// to move.
//
// SO THE STANDARD AT THIS BOUNDARY IS STATED HERE RATHER THAN LEFT SILENT,
// because an exemption by silence is how the live rule-1 breach went unseen
// for as long as it did:
//
//	A sensitive-named field at a codec boundary is declared []byte, never
//	string, and every handler that reads one wipes it before returning.
//	`string` is immutable in Go and cannot be wiped at all; []byte can, and
//	the wipe is the control. That is go-qrdb#409's shape and it is the
//	requirement here.
//
// THIS ANALYZER DOES NOT ENFORCE THE []byte HALF OF THAT STANDARD, AND THAT IS
// A KNOWN GAP, NOT AN OVERSIGHT. Eight `string`-typed codec fields remain on
// main (five in pkg/api, three in sdk/go/transport). Converting them is a
// WIRE-FORMAT BREAK — encoding/json renders []byte as base64 — so it is
// go-qrdb#409's kind of change, breaking and deliberate, not something to
// smuggle into the commit that arms a linter. Narrowing the clause to "string
// at a codec boundary is still a finding" is the stronger rule and is the
// right end state; it is tracked separately so that it lands with the
// conversions rather than red on arrival.
//
// A tag naming the field `-` is NOT at the boundary — json:"-" means the field
// is never serialized — so it stays in scope. pkg/messaging/delta_test.go's
// json:"-" fixture is the instance that proves the clause reads the tag rather
// than merely noticing one.
func codecTaggedScope(tag *ast.BasicLit) bool {
	if tag == nil {
		return false
	}
	// A struct tag literal is a raw or interpreted string; Unquote handles
	// both, and a tag that will not unquote is not a tag this can read.
	unquoted, err := strconv.Unquote(tag.Value)
	if err != nil {
		return false
	}
	st := reflect.StructTag(unquoted)
	for _, key := range codecTags {
		value, ok := st.Lookup(key)
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(value, ",")
		if name == "-" {
			// Explicitly excluded from the wire: not a boundary field.
			continue
		}
		return true
	}
	return false
}

// flagBinderPkgs are the import paths whose `...Var` functions bind a
// command-line flag to a variable.
var flagBinderPkgs = map[string]struct{}{
	"flag":                   {},
	"github.com/spf13/pflag": {},
}

// flagBoundFields returns the struct fields in this package whose address is
// passed to a command-line flag binder.
//
// S3 — COMMAND-LINE FLAG TARGETS.
//
// cmd/qrdb/barrier.go keeps each subcommand's flags on a struct so tests can
// build identical input vectors, and three of those structs have a
// `passphrase string` that `cmd.Flags().StringVar(&f.passphrase, ...)` writes.
// pflag needs a *string; there is no BufferVar and there cannot be one.
//
// The security argument is the same as S2's and it is stronger here. The value
// of --passphrase is in os.Args before main runs: the Go runtime copied it out
// of the kernel's argv at process start, it is unwipeable, and on Linux it is
// world-readable through /proc/<pid>/cmdline for the life of the process. A
// *secure.Buffer on the flags struct would mlock a SECOND copy of something
// already published to every user on the box. The defensible fix at this site
// is not to accept the material on argv at all — read it from a PIN backend,
// a terminal or stdin — and that is a CLI behaviour change, not a field type.
//
// DETECTION IS BY BINDING, NOT BY NAME OR PATH. Only a field whose address
// actually reaches flag.*Var or pflag.*Var is exempt. A field that merely sits
// next to one in the same struct is still reported, and a field that stops
// being bound to a flag becomes a finding again on the next run. That is why
// this is a pre-pass over the package rather than a list of structs.
func flagBoundFields(pass *analysis.Pass, insp *inspector.Inspector) map[types.Object]struct{} {
	bound := make(map[types.Object]struct{})
	insp.Preorder([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node) {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !isFlagBinderName(sel.Sel.Name) {
			return
		}
		if !isFlagBinderReceiver(pass, sel.X) {
			return
		}
		addr, ok := call.Args[0].(*ast.UnaryExpr)
		if !ok || addr.Op != token.AND {
			return
		}
		target, ok := addr.X.(*ast.SelectorExpr)
		if !ok {
			return
		}
		field, ok := pass.TypesInfo.ObjectOf(target.Sel).(*types.Var)
		if !ok || !field.IsField() {
			return
		}
		bound[field] = struct{}{}
	})
	return bound
}

// isFlagBinderName reports whether name is a flag-binding function name.
//
// Both flag and pflag spell every binder `<Type>Var`, with pflag adding a
// `<Type>VarP` shorthand form and both packages exposing a bare `Var` for a
// custom flag.Value. The suffix is therefore the whole convention, and a
// suffix test is what keeps this from being a list of the thirty-odd concrete
// types that would go stale the moment pflag adds one.
func isFlagBinderName(name string) bool {
	return strings.HasSuffix(name, "Var") || strings.HasSuffix(name, "VarP")
}

// isFlagBinderReceiver reports whether expr denotes the flag or pflag package,
// or a value whose type is declared in one of them.
//
// The receiver check is what stops isFlagBinderName's suffix test from
// matching an unrelated method that happens to end in Var. Both spellings this
// tree uses are covered: `flag.StringVar(...)`, where the receiver is a
// package name, and `cmd.Flags().StringVar(...)`, where it is a
// *pflag.FlagSet.
func isFlagBinderReceiver(pass *analysis.Pass, expr ast.Expr) bool {
	if ident, ok := expr.(*ast.Ident); ok {
		if pkgName, isPkg := pass.TypesInfo.ObjectOf(ident).(*types.PkgName); isPkg {
			_, hit := flagBinderPkgs[pkgName.Imported().Path()]
			return hit
		}
	}
	typ := pass.TypesInfo.TypeOf(expr)
	if typ == nil {
		return false
	}
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}
	_, hit := flagBinderPkgs[named.Obj().Pkg().Path()]
	return hit
}

// testFileScope reports whether filename is a Go test file.
//
// S4 — TEST FILES, RULE 3 ONLY.
//
// THIS CLAUSE RESTS ON A DIFFERENT ARGUMENT FROM S1-S3 and is deliberately not
// folded in with them. It is not "some other mechanism owns the allocation";
// it is that rule 3's THREAT does not exist in a test binary.
//
// Rule 3 protects a long-running server's live key material from memory
// disclosure — a core dump, a swapped page, /proc/<pid>/mem, a heap in a crash
// report. A test fixture's sensitive-named field holds a literal from the test
// source, which is in the binary's read-only data BEFORE the field exists;
// changing the field's type moves nothing out of the image, and no production
// secret is ever in that process.
//
// It is also the only clause with sites that cannot be satisfied at all rather
// than merely pointlessly. pkg/secret walks arbitrary caller config structs
// resolving cred:// references in STRING fields, so its fixture must be
// `Password string` or the test stops testing the feature; pkg/index's
// reflection fixture is the same shape. Three of the nine sites are unfixable
// in this sense, which is what makes this a scope decision rather than a
// convenience.
//
// RULES 1 AND 2 KEEP APPLYING TO TESTS, WITHOUT EXCEPTION, and that asymmetry
// is the point rather than an inconsistency:
//
//   - Rule 1 is a CONTAINMENT rule. A raw memguard import is a build-level
//     dependency edge with a PROCESS-WIDE effect — one memguard.Purge()
//     destroys every live enclave in the process, test binary included, which
//     is exactly how it behaves in production (go-qrdb#114, go-qrdb#74). The
//     one live breach on main when this was armed was rule 1 in a test file,
//     and it had sat unreported because nothing ran the checker. Exempting
//     tests from rule 1 would have made arming the gate a no-op on the single
//     most serious finding in the set.
//   - Rule 3 is a REPRESENTATION rule about a value's lifetime in a
//     long-running process. That property is what a test binary does not have.
//
// Only files named *_test.go are in this clause. A helper under pkg/testutil
// or test/, in a file without the suffix, is production-shaped code by this
// clause's reckoning and stays fully in scope — which is the floor that keeps
// "put it in a helper" from being a way out.
func testFileScope(filename string) bool {
	return strings.HasSuffix(filename, "_test.go")
}
