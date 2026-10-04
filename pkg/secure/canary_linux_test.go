// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

//go:build linux

package secure

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unsafe"
)

// TestCanary_SecretNeverOnHeap generates a 64-byte random needle, stashes
// it in a Secret, forces two GC cycles, and then scans every writable
// region of /proc/self/mem for the needle. The assertion is that the
// plaintext needle is NOT found anywhere except inside expected
// (excluded by address) and the scanner's own ReadAt buffer (excluded
// by address). After destroying the Secret, a second scan confirms the
// needle is still not found. expected is retained across both scans so
// the scanner has something to search for.
//
// This is the gold-standard zero-on-heap guarantee. It runs under Linux
// only because /proc/self/mem is Linux-specific.
func TestCanary_SecretNeverOnHeap(t *testing.T) {
	raw := make([]byte, 64)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	// expected is our reference copy — the only legitimate plaintext
	// copy of the needle. We record its address range so the scanner
	// can exclude self-matches.
	expected := append([]byte(nil), raw...)
	// #nosec G103 -- the address is recorded only to exclude this reference copy from the memory scan; it is never dereferenced.
	exStart := uintptr(unsafe.Pointer(&expected[0]))
	exLen := uintptr(len(expected))

	sec, err := NewSecret(context.Background(), raw)
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	// Encourage reclamation of transient plaintext allocations in the
	// Go heap; the memguard enclave lives outside it.
	runtime.GC()
	runtime.GC()

	hits, scanErr := scanSelfMemFor(expected, exStart, exLen)
	if scanErr != nil {
		if isMemAccessDenied(scanErr) {
			t.Skipf("/proc/self/mem not accessible: %v", scanErr)
		}
		t.Fatalf("scan /proc/self/mem: %v", scanErr)
	}
	if len(hits) != 0 {
		t.Fatalf("needle found on heap while Secret is live: %v", hits)
	}

	// Destroy the Secret and rescan.
	sec.Destroy()
	runtime.GC()
	runtime.GC()

	hits, scanErr = scanSelfMemFor(expected, exStart, exLen)
	if scanErr != nil {
		if isMemAccessDenied(scanErr) {
			t.Skipf("/proc/self/mem not accessible post-destroy: %v", scanErr)
		}
		t.Fatalf("scan /proc/self/mem post-destroy: %v", scanErr)
	}
	if len(hits) != 0 {
		t.Fatalf("needle found on heap after Destroy: %v", hits)
	}

	Wipe(expected)
}

// memRegion describes a mapping from /proc/self/maps.
type memRegion struct {
	start, end uint64
	path       string
	perms      string
}

// scanSelfMemFor reads every writable region of the current process's
// memory and returns (region, offset) tuples where needle appears. It
// excludes:
//   - Any match entirely inside [excludeAddr, excludeAddr+excludeLen)
//     (the caller's reference copy).
//   - Any match entirely inside the scanner's own per-region ReadAt
//     buffer, which necessarily contains a transient copy while we scan.
//
// An empty return means the needle is not present outside those ranges.
func scanSelfMemFor(needle []byte, excludeAddr, excludeLen uintptr) ([]string, error) {
	regions, err := readSelfMaps()
	if err != nil {
		return nil, err
	}

	f, err := os.Open("/proc/self/mem")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var hits []string
	for _, r := range regions {
		if !strings.Contains(r.perms, "w") {
			continue
		}
		if r.path == "[vvar]" || r.path == "[vsyscall]" || r.path == "[vdso]" {
			continue
		}

		size := r.end - r.start
		if size == 0 || size > maxScanRegion {
			continue
		}

		buf := make([]byte, size)
		// Record the scanner buffer address range so matches landing in
		// it (in the region we're currently reading) are excluded.
		// #nosec G103 -- the address is recorded only to exclude the scanner's own buffer from the matches; it is never dereferenced.
		bufAddr := uintptr(unsafe.Pointer(&buf[0]))
		bufLen := uintptr(len(buf))

		n, readErr := f.ReadAt(buf, int64(r.start))
		if readErr != nil && readErr != io.EOF {
			// Some regions (I/O mappings) reject reads; skip.
			continue
		}
		slice := buf[:n]
		start := 0
		for {
			rel := bytes.Index(slice[start:], needle)
			if rel < 0 {
				break
			}
			abs := start + rel
			va := uintptr(r.start) + uintptr(abs)
			nLen := uintptr(len(needle))
			// Exclude matches in the caller's reference copy.
			if excludeLen > 0 && va >= excludeAddr && va+nLen <= excludeAddr+excludeLen {
				start = abs + 1
				continue
			}
			// Exclude matches in the scanner's own ReadAt buffer.
			if bufLen > 0 && va >= bufAddr && va+nLen <= bufAddr+bufLen {
				start = abs + 1
				continue
			}
			hits = append(hits, fmt.Sprintf("%s @ va=0x%x off=%d", r.path, va, abs))
			start = abs + 1
		}
	}
	return hits, nil
}

// maxScanRegion caps per-region reads so an anomalously large mapping
// doesn't blow memory during the scan.
const maxScanRegion = 256 * 1024 * 1024

// readSelfMaps parses /proc/self/maps, returning writable regions only.
func readSelfMaps() ([]memRegion, error) {
	f, err := os.Open("/proc/self/maps")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []memRegion
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 256*1024), 1024*1024)
	for s.Scan() {
		line := s.Text()
		parts := strings.Fields(line)
		if len(parts) < 5 {
			continue
		}
		rangePart := parts[0]
		perms := parts[1]
		if !strings.Contains(perms, "w") {
			continue
		}
		dash := strings.IndexByte(rangePart, '-')
		if dash < 0 {
			continue
		}
		start, err := strconv.ParseUint(rangePart[:dash], 16, 64)
		if err != nil {
			continue
		}
		end, err := strconv.ParseUint(rangePart[dash+1:], 16, 64)
		if err != nil {
			continue
		}
		path := ""
		if len(parts) >= 6 {
			path = strings.Join(parts[5:], " ")
		}
		out = append(out, memRegion{start: start, end: end, path: path, perms: perms})
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// isMemAccessDenied reports whether err indicates a sandbox / yama
// ptrace denial on /proc/self/mem.
func isMemAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	if os.IsPermission(err) {
		return true
	}
	return strings.Contains(err.Error(), "operation not permitted") ||
		strings.Contains(err.Error(), "permission denied")
}
