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
	"testing"

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

// TestAnalyzer_AllowedMemguard verifies that packages whose import path
// contains the go-qrdb/pkg/secure substring are exempt from the memguard ban
// (covers the inPkgSecure = true branch in run).
func TestAnalyzer_AllowedMemguard(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer,
		"github.com/jeremyhahn/go-qrdb/pkg/secure/allowed_memguard")
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
