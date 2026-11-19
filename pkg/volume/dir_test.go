// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirProvider_OpenValidDirectory(t *testing.T) {
	dir := t.TempDir()
	p := NewDirProvider()

	v, err := p.Open(context.Background(), Spec{Pool: "fast", NodeID: "node-1", Path: dir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if v.Root() != filepath.Clean(dir) {
		t.Fatalf("Root() = %q, want %q", v.Root(), dir)
	}

	usage, err := v.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.SizeBytes <= 0 {
		t.Fatalf("Usage().SizeBytes = %d, want > 0", usage.SizeBytes)
	}

	health := v.Health(context.Background())
	if health.State != StateReady {
		t.Fatalf("Health().State = %v, want StateReady (detail: %s)", health.State, health.Detail)
	}
	if health.CheckedAt.IsZero() {
		t.Fatal("Health().CheckedAt is zero")
	}
}

func TestDirProvider_OpenIsIdempotentPerPool(t *testing.T) {
	dir := t.TempDir()
	p := NewDirProvider()
	spec := Spec{Pool: "fast", Path: dir}

	v1, err := p.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	v2, err := p.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open (second): %v", err)
	}
	if v1 != v2 {
		t.Fatal("Open for the same pool returned two different volumes")
	}
}

func TestDirProvider_OpenEmptyPool(t *testing.T) {
	p := NewDirProvider()
	_, err := p.Open(context.Background(), Spec{Pool: "", Path: t.TempDir()})
	var target *InvalidSpecError
	if !errors.As(err, &target) {
		t.Fatalf("Open(empty pool) error = %v, want *InvalidSpecError", err)
	}
}

func TestDirProvider_OpenMissingPath(t *testing.T) {
	p := NewDirProvider()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := p.Open(context.Background(), Spec{Pool: "fast", Path: missing})
	var target *OpenError
	if !errors.As(err, &target) {
		t.Fatalf("Open(missing path) error = %v, want *OpenError", err)
	}
}

func TestDirProvider_OpenPathIsAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	p := NewDirProvider()
	_, err := p.Open(context.Background(), Spec{Pool: "fast", Path: file})
	var target *OpenError
	if !errors.As(err, &target) {
		t.Fatalf("Open(file path) error = %v, want *OpenError", err)
	}
}

func TestDirProvider_OpenRelativePathRefused(t *testing.T) {
	p := NewDirProvider()
	_, err := p.Open(context.Background(), Spec{Pool: "fast", Path: "relative/path"})
	var target *OpenError
	if !errors.As(err, &target) {
		t.Fatalf("Open(relative path) error = %v, want *OpenError", err)
	}
}

func TestDirProvider_OpenNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "readonly")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // #nosec G302 -- restores the mode t.TempDir() needs to remove the directory; not a secret file's mode

	p := NewDirProvider()
	_, err := p.Open(context.Background(), Spec{Pool: "fast", Path: dir})
	var target *OpenError
	if !errors.As(err, &target) {
		t.Fatalf("Open(read-only dir) error = %v, want *OpenError", err)
	}
}

func TestDirVolume_CloseThenReopenGetsFreshVolume(t *testing.T) {
	dir := t.TempDir()
	p := NewDirProvider()
	spec := Spec{Pool: "fast", Path: dir}

	v1, err := p.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := v1.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close is idempotent.
	if err := v1.Close(context.Background()); err != nil {
		t.Fatalf("Close (second): %v", err)
	}

	v2, err := p.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open (after close): %v", err)
	}
	if v1 == v2 {
		t.Fatal("Open after Close returned the closed volume instead of a fresh one")
	}
}

func TestDirVolume_UsageAndHealthAfterClose(t *testing.T) {
	dir := t.TempDir()
	p := NewDirProvider()
	v, err := p.Open(context.Background(), Spec{Pool: "fast", Path: dir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := v.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := v.Usage(context.Background()); err == nil {
		t.Fatal("Usage after Close should fail")
	}
	health := v.Health(context.Background())
	if health.State != StateUnavailable {
		t.Fatalf("Health().State after Close = %v, want StateUnavailable", health.State)
	}
}

func TestDirVolume_HealthReportsUnavailableWhenDirectoryRemoved(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "gone")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	p := NewDirProvider()
	v, err := p.Open(context.Background(), Spec{Pool: "fast", Path: dir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	health := v.Health(context.Background())
	if health.State != StateUnavailable {
		t.Fatalf("Health().State = %v, want StateUnavailable", health.State)
	}
	if health.Detail == "" {
		t.Fatal("Health().Detail should explain the failure")
	}
	// Security review finding 8: Health is replicated into a wire-facing
	// NodeReport, so Detail must never embed this node's own filesystem
	// path -- only the underlying OS error (e.g. "no such file or
	// directory"), which os.Stat's *fs.PathError would otherwise carry.
	if strings.Contains(health.Detail, dir) {
		t.Fatalf("Health().Detail = %q, must never contain the node's filesystem path %q", health.Detail, dir)
	}
}
