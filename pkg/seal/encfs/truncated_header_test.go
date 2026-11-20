// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package encfs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
)

// TestFramed_SyncedLogTruncatedIntoHeaderRefused writes and syncs a record to
// a WAL and a MANIFEST, then truncates each into its header without changing
// the retained bytes. A record was acknowledged, so the file is corrupt, not
// a crash inside Create: Open and OpenReadWrite must both refuse it with
// ErrTruncatedHeader, and the read-write open must leave the bytes untouched.
func TestFramed_SyncedLogTruncatedIntoHeaderRefused(t *testing.T) {
	b := newFakeBarrier(t)
	for _, name := range []string{"000009.log", "MANIFEST-000005"} {
		dir := t.TempDir()
		path := filepath.Join(dir, name)
		fsys := New(filesys.Default, b)
		w, err := fsys.Create(path)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := w.Write([]byte("acknowledged record")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := w.Sync(); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		img, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		hdrSize := int(binary.LittleEndian.Uint16(img[offHeaderSize:]))
		for _, cut := range []int{1, lenMagic, fixedHeaderSize - 1, fixedHeaderSize, offWrappedDEKLen + 4, hdrSize - 1} {
			t.Run(fmt.Sprintf("%s/cut=%d", name, cut), func(t *testing.T) {
				cutPath := filepath.Join(t.TempDir(), name)
				if err := os.WriteFile(cutPath, img[:cut], 0o600); err != nil {
					t.Fatalf("write truncated log: %v", err)
				}
				if r, err := fsys.Open(cutPath); !errors.Is(err, ErrTruncatedHeader) {
					if r != nil {
						_ = r.Close()
					}
					t.Fatalf("Open truncated synced log: err %v, want ErrTruncatedHeader", err)
				}
				if rw, err := fsys.OpenReadWrite(cutPath); !errors.Is(err, ErrTruncatedHeader) {
					if rw != nil {
						_ = rw.Close()
					}
					t.Fatalf("OpenReadWrite truncated synced log: err %v, want ErrTruncatedHeader", err)
				}
				after, err := os.ReadFile(cutPath)
				if err != nil {
					t.Fatalf("reread: %v", err)
				}
				if !bytes.Equal(after, img[:cut]) {
					t.Fatal("refused open rewrote the truncated log")
				}
			})
		}
	}
}

// TestFramed_SyncedLogTruncatedIntoCommitSlotsRefused writes and syncs a
// record to a framed log, then cuts the file inside its commit slots: at the
// end of the header, partway through the first slot, at the end of the first
// slot and one byte short of the second. The stamped magic proves Create
// synced both slots, so a missing or partial slot is lost data, not an
// unfinished Create: both opens must refuse it with ErrTruncatedHeader rather
// than open an empty log, and the read-write open must leave the bytes
// untouched.
func TestFramed_SyncedLogTruncatedIntoCommitSlotsRefused(t *testing.T) {
	b := newFakeBarrier(t)
	for _, name := range []string{"000009.log", "MANIFEST-000005"} {
		dir := t.TempDir()
		path := filepath.Join(dir, name)
		fsys := New(filesys.Default, b)
		w, err := fsys.Create(path)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := w.Write([]byte("acknowledged record")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := w.Sync(); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		img, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		hdrSize := int(binary.LittleEndian.Uint16(img[offHeaderSize:]))
		start := hdrSize + commitSlots*commitSlotSize
		if len(img) <= start {
			t.Fatalf("synced log is %d bytes, want frames past %d", len(img), start)
		}
		for _, cut := range []int{hdrSize, hdrSize + 1, hdrSize + commitSlotSize - 1, hdrSize + commitSlotSize, start - 1} {
			t.Run(fmt.Sprintf("%s/cut=%d", name, cut), func(t *testing.T) {
				cutPath := filepath.Join(t.TempDir(), name)
				if err := os.WriteFile(cutPath, img[:cut], 0o600); err != nil {
					t.Fatalf("write truncated log: %v", err)
				}
				if r, err := fsys.Open(cutPath); !errors.Is(err, ErrTruncatedHeader) {
					if r != nil {
						_ = r.Close()
					}
					t.Fatalf("Open log cut in its commit slots: err %v, want ErrTruncatedHeader", err)
				}
				if rw, err := fsys.OpenReadWrite(cutPath); !errors.Is(err, ErrTruncatedHeader) {
					if rw != nil {
						_ = rw.Close()
					}
					t.Fatalf("OpenReadWrite log cut in its commit slots: err %v, want ErrTruncatedHeader", err)
				}
				after, err := os.ReadFile(cutPath)
				if err != nil {
					t.Fatalf("reread: %v", err)
				}
				if !bytes.Equal(after, img[:cut]) {
					t.Fatal("refused open rewrote the truncated log")
				}
			})
		}
	}
}
