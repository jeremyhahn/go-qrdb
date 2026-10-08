// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package analyzer

import (
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer_BadMemguard(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "bad_memguard")
}

func TestAnalyzer_BadField(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "bad_field")
}

func TestAnalyzer_BadSSH(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "bad_ssh")
}

func TestAnalyzer_Good(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "good")
}

// TestAnalyzer_BadSSHAlias verifies that an aliased ssh import is also
// flagged (covers the imp.Name != nil branch in run).
func TestAnalyzer_BadSSHAlias(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "bad_ssh_alias")
}

// TestAnalyzer_GoodFieldTypes verifies that sensitive-looking field names with
// non-sensitive types (fixed arrays, pointer, non-byte slices, non-string
// scalars) are not flagged (covers uncovered branches of isSensitiveType).
func TestAnalyzer_GoodFieldTypes(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "good_field_types")
}

// TestAnalyzer_GoodSSHNonIdent verifies that a ParsePrivateKey call whose
// receiver is a chained selector (e.g. p.helper.ParsePrivateKey) is not
// flagged because the LHS is not a plain identifier (covers the
// sel.X.(*ast.Ident) !ok early-return in run rule 2).
func TestAnalyzer_GoodSSHNonIdent(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "good_ssh_nonident")
}

// TestCopyOutAnalyzer verifies securecopy (go-qrdb#459) end to end: every
// copy-out shape it names is reported when nothing wipes, returns or consumes
// it; every discharge it names silences it; a wipe or an argument-evaluating
// defer written BEFORE the copy does not; and the consumes/returnsCopy facts
// it infers are the ones expected, including across a package boundary.
func TestCopyOutAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), CopyOutAnalyzer,
		"copyout_cases", "github.com/jeremyhahn/go-qrdb/pkg/secure")
}

// TestIsSensitiveType exercises every branch of isSensitiveType directly.
func TestIsSensitiveType(t *testing.T) {
	t.Run("string is sensitive", func(t *testing.T) {
		expr := &ast.Ident{Name: "string"}
		if !isSensitiveType(expr) {
			t.Error("expected string to be sensitive")
		}
	})
	t.Run("non-string ident is not sensitive", func(t *testing.T) {
		expr := &ast.Ident{Name: "int"}
		if isSensitiveType(expr) {
			t.Error("expected int to not be sensitive")
		}
	})
	t.Run("slice of byte is sensitive", func(t *testing.T) {
		expr := &ast.ArrayType{
			Elt: &ast.Ident{Name: "byte"},
		}
		if !isSensitiveType(expr) {
			t.Error("expected []byte to be sensitive")
		}
	})
	t.Run("fixed-size byte array is not sensitive", func(t *testing.T) {
		expr := &ast.ArrayType{
			Len: &ast.BasicLit{Kind: token.INT, Value: "32"},
			Elt: &ast.Ident{Name: "byte"},
		}
		if isSensitiveType(expr) {
			t.Error("expected [32]byte to not be sensitive")
		}
	})
	t.Run("slice of non-byte ident is not sensitive", func(t *testing.T) {
		expr := &ast.ArrayType{
			Elt: &ast.Ident{Name: "int"},
		}
		if isSensitiveType(expr) {
			t.Error("expected []int to not be sensitive")
		}
	})
	t.Run("slice with non-ident element is not sensitive", func(t *testing.T) {
		// []ssh.Signer: element is a SelectorExpr, not an Ident.
		expr := &ast.ArrayType{
			Elt: &ast.SelectorExpr{
				X:   &ast.Ident{Name: "ssh"},
				Sel: &ast.Ident{Name: "Signer"},
			},
		}
		if isSensitiveType(expr) {
			t.Error("expected []ssh.Signer to not be sensitive")
		}
	})
	t.Run("pointer type is not sensitive", func(t *testing.T) {
		// *string: neither Ident nor ArrayType at the top level.
		expr := &ast.StarExpr{X: &ast.Ident{Name: "string"}}
		if isSensitiveType(expr) {
			t.Error("expected *string to not be sensitive")
		}
	})
}

// TestTypeDescription exercises every branch of typeDescription directly,
// including the "unknown" fallback for composite types.
func TestTypeDescription(t *testing.T) {
	t.Run("ident returns name", func(t *testing.T) {
		expr := &ast.Ident{Name: "string"}
		if got := typeDescription(expr); got != "string" {
			t.Errorf("expected %q, got %q", "string", got)
		}
	})
	t.Run("array type with ident element returns slice notation", func(t *testing.T) {
		expr := &ast.ArrayType{Elt: &ast.Ident{Name: "byte"}}
		if got := typeDescription(expr); got != "[]byte" {
			t.Errorf("expected %q, got %q", "[]byte", got)
		}
	})
	t.Run("array type with non-ident element returns unknown", func(t *testing.T) {
		// []ssh.Signer: element is a SelectorExpr, falls through to "unknown".
		expr := &ast.ArrayType{
			Elt: &ast.SelectorExpr{
				X:   &ast.Ident{Name: "ssh"},
				Sel: &ast.Ident{Name: "Signer"},
			},
		}
		if got := typeDescription(expr); got != "unknown" {
			t.Errorf("expected %q, got %q", "unknown", got)
		}
	})
	t.Run("pointer type returns unknown", func(t *testing.T) {
		expr := &ast.StarExpr{X: &ast.Ident{Name: "string"}}
		if got := typeDescription(expr); got != "unknown" {
			t.Errorf("expected %q, got %q", "unknown", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Rule 3 scope (go-qrdb#429)
//
// One test per clause, each asserting BOTH directions: the population the
// clause is for produces no diagnostic, and a neighbouring declaration that
// does not meet the clause still does. A scope test that only checks the
// silence would pass just as well if the rule had been deleted.
// ---------------------------------------------------------------------------

// TestAnalyzer_ScopeGenerated verifies S1: a file carrying the toolchain's
// generated-code marker is out of scope for rule 3, and a hand-written file in
// the same package is not.
func TestAnalyzer_ScopeGenerated(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "scope_generated")
}

// TestAnalyzer_ScopeCodecTag verifies S2: a field a codec materialises is out
// of scope, an untagged neighbour is not, a tag naming no codec is not, and
// json:"-" — which means the field never reaches the wire — is not.
func TestAnalyzer_ScopeCodecTag(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "scope_codec_tag")
}

// TestAnalyzer_ScopeFlagTarget verifies S3: a field whose address reaches
// flag.*Var or pflag.*Var is out of scope, while an unbound field in the same
// struct and a look-alike method on an unrelated type are not.
func TestAnalyzer_ScopeFlagTarget(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "scope_flag_target")
}

// TestAnalyzer_ScopeTestFile verifies S4 and the asymmetry it rests on: rule 3
// is out of scope in a _test.go file, rules 1 and 2 are still enforced there,
// and a non-test file in the same package is fully in scope.
func TestAnalyzer_ScopeTestFile(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "scope_test_file")
}

// TestCodecTaggedScope exercises codecTaggedScope's guards directly. Two of
// them are unreachable from valid Go source — an absent tag reaches the
// function only through the struct walk's own nil, and a literal the parser
// accepted always unquotes — so a synthetic node is the only way to drive
// them. Both must fail CLOSED: no readable codec tag means no evidence of a
// serialization boundary, and rule 3 stays in force.
func TestCodecTaggedScope(t *testing.T) {
	t.Run("absent tag is not a boundary", func(t *testing.T) {
		if codecTaggedScope(nil) {
			t.Error("a field with no tag was treated as decoder-owned")
		}
	})
	t.Run("unquotable literal is not a boundary", func(t *testing.T) {
		// A literal the go parser would never produce: no delimiters at all.
		lit := &ast.BasicLit{Kind: token.STRING, Value: `json:"passphrase"`}
		if codecTaggedScope(lit) {
			t.Error("an unreadable tag was treated as decoder-owned")
		}
	})
	t.Run("codec tag is a boundary", func(t *testing.T) {
		lit := &ast.BasicLit{Kind: token.STRING, Value: "`json:\"passphrase\"`"}
		if !codecTaggedScope(lit) {
			t.Error("a json-tagged field was not treated as decoder-owned")
		}
	})
	t.Run("dash name is not a boundary", func(t *testing.T) {
		lit := &ast.BasicLit{Kind: token.STRING, Value: "`json:\"-\"`"}
		if codecTaggedScope(lit) {
			t.Error(`json:"-" means the field never reaches the wire`)
		}
	})
}

// TestIsFlagBinderName covers both spellings the flag packages use and the
// names that merely resemble them.
func TestIsFlagBinderName(t *testing.T) {
	for _, name := range []string{"StringVar", "StringVarP", "Var", "VarP", "BytesHexVar"} {
		if !isFlagBinderName(name) {
			t.Errorf("isFlagBinderName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"String", "Varies", "VarPP", "Parse"} {
		if isFlagBinderName(name) {
			t.Errorf("isFlagBinderName(%q) = true, want false", name)
		}
	}
}

// TestIsFlagBinderReceiver_Untyped drives the branch where the receiver
// expression has no type recorded. It cannot arise from a package that
// typechecked, which is exactly why it needs driving: if the guard were
// dropped, a nil type would panic the analyzer rather than report anything,
// and a vet plugin that panics takes the whole Lint job with it.
func TestIsFlagBinderReceiver_Untyped(t *testing.T) {
	pass := &analysis.Pass{TypesInfo: &types.Info{
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Types:      map[ast.Expr]types.TypeAndValue{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}}
	if isFlagBinderReceiver(pass, &ast.Ident{Name: "fs"}) {
		t.Error("an identifier with no recorded type was treated as a flag set")
	}
	if isFlagBinderReceiver(pass, &ast.CallExpr{Fun: &ast.Ident{Name: "Flags"}}) {
		t.Error("an expression with no recorded type was treated as a flag set")
	}
}
