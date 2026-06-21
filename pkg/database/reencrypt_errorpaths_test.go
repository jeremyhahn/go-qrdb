// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestReencryptStoreFiles_SrcDirMissing returns a ReencryptOpenError when the
// source directory cannot be read.
func TestReencryptStoreFiles_SrcDirMissing(t *testing.T) {
	srcEnc := newNoneEncryptor(t, "ep-none/root-key")
	dstEnc := newKeyedEncryptor(t, "ep-keyed/root-key", "pw")
	_, err := ReencryptStoreFiles(context.Background(),
		filepath.Join(t.TempDir(), "does-not-exist"), srcEnc,
		filepath.Join(t.TempDir(), "dst"), dstEnc)
	var openErr *ReencryptOpenError
	if !errors.As(err, &openErr) {
		t.Fatalf("err = %v, want *ReencryptOpenError", err)
	}
}

// TestReencryptStoreFiles_CtxCancelled aborts with a ReencryptCopyError wrapping
// the context error when the context is already cancelled.
func TestReencryptStoreFiles_CtxCancelled(t *testing.T) {
	srcEnc := newNoneEncryptor(t, "ep2-none/root-key")
	dstEnc := newKeyedEncryptor(t, "ep2-keyed/root-key", "pw")

	srcDir := filepath.Join(t.TempDir(), "store")
	mustMkdir(t, srcDir)
	mustWriteFile(t, filepath.Join(srcDir, "000001.sst"), "data")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ReencryptStoreFiles(ctx, srcDir, srcEnc, filepath.Join(t.TempDir(), "dst"), dstEnc)
	var copyErr *ReencryptCopyError
	if !errors.As(err, &copyErr) {
		t.Fatalf("err = %v, want *ReencryptCopyError", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want wrapped context.Canceled", err)
	}
}

// TestReencryptStoreFiles_SkipsLOCK confirms a Pebble LOCK file is never copied
// (Pebble re-creates its own on open).
func TestReencryptStoreFiles_SkipsLOCK(t *testing.T) {
	srcEnc := newNoneEncryptor(t, "ep3-none/root-key")
	dstEnc := newKeyedEncryptor(t, "ep3-keyed/root-key", "pw")

	srcDir := filepath.Join(t.TempDir(), "store")
	mustMkdir(t, srcDir)
	mustWriteFile(t, filepath.Join(srcDir, "LOCK"), "lockbytes")
	mustWriteFile(t, filepath.Join(srcDir, "CURRENT"), "plaintext-manifest-ptr")

	dstDir := filepath.Join(t.TempDir(), "dst")
	n, err := ReencryptStoreFiles(context.Background(), srcDir, srcEnc, dstDir, dstEnc)
	if err != nil {
		t.Fatalf("ReencryptStoreFiles: %v", err)
	}
	if n != 1 { // CURRENT copied, LOCK skipped
		t.Fatalf("copied %d files, want 1 (LOCK skipped)", n)
	}
	if _, serr := os.Stat(filepath.Join(dstDir, "LOCK")); !os.IsNotExist(serr) {
		t.Fatalf("LOCK must not be copied to the destination")
	}
}

// TestCopyFileRaw_SourceMissing covers the open-error path of copyFileRaw.
func TestCopyFileRaw_SourceMissing(t *testing.T) {
	err := copyFileRaw(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "out"))
	if err == nil {
		t.Fatal("copyFileRaw with a missing source must error")
	}
}

// TestCopyFileRaw_DestUnwritable covers the create-error path of copyFileRaw
// (destination parent directory does not exist).
func TestCopyFileRaw_DestUnwritable(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	mustWriteFile(t, src, "x")
	err := copyFileRaw(src, filepath.Join(t.TempDir(), "missing-parent", "out"))
	if err == nil {
		t.Fatal("copyFileRaw with an unwritable destination must error")
	}
}

// TestReencryptPebbleStore_SrcMissing covers the source-open error path.
func TestReencryptPebbleStore_SrcMissing(t *testing.T) {
	srcEnc := newNoneEncryptor(t, "pp-none/root-key")
	dstEnc := newKeyedEncryptor(t, "pp-keyed/root-key", "pw")
	_, err := ReencryptPebbleStore(context.Background(),
		filepath.Join(t.TempDir(), "nope"), srcEnc, filepath.Join(t.TempDir(), "dst"), dstEnc)
	var openErr *ReencryptOpenError
	if !errors.As(err, &openErr) {
		t.Fatalf("err = %v, want *ReencryptOpenError", err)
	}
}

// TestReencryptPebbleStore_CtxCancelled aborts the logical copy with a
// ReencryptCopyError wrapping the context error.
func TestReencryptPebbleStore_CtxCancelled(t *testing.T) {
	srcEnc := newNoneEncryptor(t, "pp2-none/root-key")
	dstEnc := newKeyedEncryptor(t, "pp2-keyed/root-key", "pw")

	srcDir := filepath.Join(t.TempDir(), "store")
	writeStore(t, srcDir, srcEnc, map[string]string{"a": "1", "b": "2", "c": "3"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ReencryptPebbleStore(ctx, srcDir, srcEnc, filepath.Join(t.TempDir(), "dst"), dstEnc)
	var copyErr *ReencryptCopyError
	if !errors.As(err, &copyErr) {
		t.Fatalf("err = %v, want *ReencryptCopyError", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want wrapped context.Canceled", err)
	}
}

// TestVerifyReencryptedStore_SrcMissing covers the source-open error path of the
// post-copy verifier.
func TestVerifyReencryptedStore_SrcMissing(t *testing.T) {
	srcEnc := newNoneEncryptor(t, "vv-none/root-key")
	dstEnc := newKeyedEncryptor(t, "vv-keyed/root-key", "pw")
	dstDir := filepath.Join(t.TempDir(), "dst")
	writeStore(t, dstDir, dstEnc, map[string]string{"a": "1"})
	err := VerifyReencryptedStore(context.Background(),
		filepath.Join(t.TempDir(), "nope"), srcEnc, dstDir, dstEnc)
	if err == nil {
		t.Fatal("VerifyReencryptedStore with a missing source must error")
	}
}

// TestRecodeFile_SourceMissing covers the srcFS.Open error path of recodeFile.
func TestRecodeFile_SourceMissing(t *testing.T) {
	srcEnc := newNoneEncryptor(t, "rc-none/root-key")
	dstEnc := newKeyedEncryptor(t, "rc-keyed/root-key", "pw")
	err := recodeFile(srcEnc.FS(), filepath.Join(t.TempDir(), "missing"),
		dstEnc.FS(), filepath.Join(t.TempDir(), "out"))
	if err == nil {
		t.Fatal("recodeFile with a missing source must error")
	}
}

// TestIsEncfsFile_Variants covers the too-short (EOF) and non-encfs branches.
func TestIsEncfsFile_Variants(t *testing.T) {
	short := filepath.Join(t.TempDir(), "short")
	mustWriteFile(t, short, "ab") // shorter than the magic
	if enc, err := isEncfsFile(short); err != nil || enc {
		t.Fatalf("isEncfsFile(short) = %v, %v; want false, nil", enc, err)
	}

	plain := filepath.Join(t.TempDir(), "plain")
	mustWriteFile(t, plain, "this is definitely not the encfs magic header")
	if enc, err := isEncfsFile(plain); err != nil || enc {
		t.Fatalf("isEncfsFile(plain) = %v, %v; want false, nil", enc, err)
	}

	if _, err := isEncfsFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("isEncfsFile(missing) must error")
	}
}
