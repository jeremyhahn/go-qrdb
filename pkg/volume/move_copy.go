// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

// This file is the node-local step of the proposed tenant move
// (docs/decisions/volume-pool-tenant-moves.md, recommendations Q2 A and
// Q4 A): a file copy of a closed replica directory from one absolute
// directory to another, fsyncing every file and directory, with a
// manifest of relative path, permission bits, size and SHA-256 built while
// copying and recomputed from the destination afterwards. Nothing calls
// it yet: there is no move journal, reconciler or transport in this build.
//
// The primitive knows nothing of pools or tenants and never deletes
// anything. On failure the destination is left as it is; the caller
// removes it through seal.BarrierRegistry.RequireInsideLocalPool.

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
	"github.com/jeremyhahn/go-qrdb/pkg/validation"
)

const (
	movePhaseCopy   = "copy"
	movePhaseVerify = "verify"
)

// ManifestEntry describes one entry of a copied tree. Path is
// slash-separated and relative to the tree's root, never empty and never
// "..". Mode holds the permission bits only. For a directory Size is 0 and
// SHA256 is the zero digest.
type ManifestEntry struct {
	Path   string
	Dir    bool
	Mode   fs.FileMode
	Size   int64
	SHA256 [32]byte
}

// Manifest lists every entry below a tree's root (the root itself is not
// an entry), sorted by Path.
type Manifest struct {
	Entries []ManifestEntry
}

// moveRootError is the cause of a MoveStepError about a root directory
// itself. It names the root by role ("source", "destination"), never by
// path, so the message carries no mount path.
type moveRootError struct {
	Root   string
	Reason string
	Err    error
}

func (e *moveRootError) Error() string {
	msg := e.Root + " directory " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *moveRootError) Unwrap() error { return e.Err }

// moveOpError is an operating-system error with its path stripped: the
// MoveStepError around it already names the root-relative entry.
type moveOpError struct {
	Op  string
	Err error
}

func (e *moveOpError) Error() string { return e.Op + ": " + e.Err.Error() }

func (e *moveOpError) Unwrap() error { return e.Err }

// unsupportedEntryError refuses an entry that is neither a regular file
// nor a directory.
type unsupportedEntryError struct {
	Kind string
}

func (e *unsupportedEntryError) Error() string {
	return "unsupported entry type " + e.Kind + ": only regular files and directories are copied"
}

// scrubPath drops the path from an *fs.PathError so no absolute path
// reaches an error message; errors.Is against the errno still works.
func scrubPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return &moveOpError{Op: pe.Op, Err: pe.Err}
	}
	return err
}

func stepErr(phase, rel string, err error) *MoveStepError {
	return &MoveStepError{Phase: phase, Path: rel, Err: scrubPath(err)}
}

func rootErr(phase, role, reason string, err error) *MoveStepError {
	return &MoveStepError{Phase: phase, Err: &moveRootError{Root: role, Reason: reason, Err: scrubPath(err)}}
}

// checkRootPath applies validation.AbsoluteCanonicalPath without letting
// the path into the message.
func checkRootPath(phase, role, p string) error {
	if err := validation.AbsoluteCanonicalPath(p); err != nil {
		// The reasons validation.PathError carries for a failed stat or a
		// symlink name the absolute path, so the move error maps every
		// reason to a fixed one and never carries the cause.
		var pe *validation.PathError
		if errors.As(err, &pe) {
			if strings.HasPrefix(pe.Reason, "must ") {
				return rootErr(phase, role, "must be an absolute canonical path", nil)
			}
			return rootErr(phase, role, "must not traverse a symlink", nil)
		}
		return rootErr(phase, role, "is not a valid path", nil)
	}
	return nil
}

// within reports whether p is base or lies below it.
func within(base, p string) bool {
	if p == base {
		return true
	}
	return strings.HasPrefix(p, strings.TrimSuffix(base, string(filepath.Separator))+string(filepath.Separator))
}

func entryKind(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "symlink"
	case m&fs.ModeNamedPipe != 0:
		return "named pipe"
	case m&fs.ModeSocket != 0:
		return "socket"
	case m&fs.ModeDevice != 0:
		return "device"
	default:
		return "irregular"
	}
}

// openRootDir validates root, checks it is an existing directory and opens
// it as an *os.Root.
func openRootDir(phase, role, root string) (*os.Root, error) {
	if err := checkRootPath(phase, role, root); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, rootErr(phase, role, "cannot be read", err)
	}
	if !info.IsDir() {
		return nil, rootErr(phase, role, "is not a directory", nil)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, rootErr(phase, role, "cannot be opened", err)
	}
	return r, nil
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return path.Join(dir, name)
}

func osName(rel string) string {
	if rel == "" {
		return "."
	}
	return filepath.FromSlash(rel)
}

func sortManifest(m *Manifest) {
	slices.SortFunc(m.Entries, func(a, b ManifestEntry) int { return strings.Compare(a.Path, b.Path) })
}

// BuildManifest walks root in lexical order and returns the manifest of
// every entry below it. root must pass validation.AbsoluteCanonicalPath and
// be an existing directory; any entry that is neither a regular file nor a
// directory fails the walk. Errors are *MoveStepError with Phase "verify".
func BuildManifest(ctx context.Context, root string) (*Manifest, error) {
	r, err := openRootDir(movePhaseVerify, "manifest", root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	m := &Manifest{}
	if err := manifestDir(ctx, r, "", m); err != nil {
		return nil, err
	}
	sortManifest(m)
	return m, nil
}

func readDirEntries(r *os.Root, rel string) ([]fs.DirEntry, fs.FileMode, error) {
	d, err := r.Open(osName(rel))
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = d.Close() }()
	info, err := d.Stat()
	if err != nil {
		return nil, 0, err
	}
	entries, err := d.ReadDir(-1)
	if err != nil {
		return nil, 0, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, info.Mode().Perm(), nil
}

func manifestDir(ctx context.Context, r *os.Root, rel string, m *Manifest) error {
	entries, _, err := readDirEntries(r, rel)
	if err != nil {
		return stepErr(movePhaseVerify, rel, err)
	}
	for _, e := range entries {
		child := joinRel(rel, e.Name())
		if err := ctx.Err(); err != nil {
			return stepErr(movePhaseVerify, child, err)
		}
		switch {
		case e.Type().IsDir():
			info, err := r.Lstat(osName(child))
			if err != nil {
				return stepErr(movePhaseVerify, child, err)
			}
			m.Entries = append(m.Entries, ManifestEntry{Path: child, Dir: true, Mode: info.Mode().Perm()})
			if err := manifestDir(ctx, r, child, m); err != nil {
				return err
			}
		case e.Type().IsRegular():
			entry, err := hashFile(r, child)
			if err != nil {
				return stepErr(movePhaseVerify, child, err)
			}
			m.Entries = append(m.Entries, entry)
		default:
			return stepErr(movePhaseVerify, child, &unsupportedEntryError{Kind: entryKind(e.Type())})
		}
	}
	return nil
}

func hashFile(r *os.Root, rel string) (ManifestEntry, error) {
	f, err := r.Open(osName(rel))
	if err != nil {
		return ManifestEntry{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return ManifestEntry{}, err
	}
	if !info.Mode().IsRegular() {
		return ManifestEntry{}, &unsupportedEntryError{Kind: entryKind(info.Mode())}
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return ManifestEntry{}, err
	}
	e := ManifestEntry{Path: rel, Mode: info.Mode().Perm(), Size: n}
	copy(e.SHA256[:], h.Sum(nil))
	return e, nil
}

// CopyTree copies the tree at src to dst and returns the manifest built
// while copying. Both paths must pass validation.AbsoluteCanonicalPath;
// src must be an existing directory, dst must not exist while its parent
// does, and neither may lie inside the other. dst is created 0700. src is
// walked in lexical order through os.OpenRoot on both trees, so no name
// escapes either root. Each regular file is copied with its permission
// bits and hashed while it is copied, then fsynced; each directory gets
// its source's permission bits and is fsynced after its entries, and
// dst's parent is fsynced last. Any entry that is not a regular file or a
// directory refuses the copy. ctx is checked before each entry.
//
// On any failure CopyTree returns at once and leaves whatever dst holds:
// it never deletes anything, and the caller removes a partial dst through
// seal.BarrierRegistry.RequireInsideLocalPool. Errors are *MoveStepError
// with Phase "copy". src is only read.
func CopyTree(ctx context.Context, src, dst string) (*Manifest, error) {
	if err := checkRootPath(movePhaseCopy, "source", src); err != nil {
		return nil, err
	}
	if err := checkRootPath(movePhaseCopy, "destination", dst); err != nil {
		return nil, err
	}
	if within(src, dst) {
		return nil, rootErr(movePhaseCopy, "destination", "lies inside the source", nil)
	}
	if within(dst, src) {
		return nil, rootErr(movePhaseCopy, "source", "lies inside the destination", nil)
	}
	if _, err := os.Lstat(dst); err == nil {
		return nil, rootErr(movePhaseCopy, "destination", "already exists", nil)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, rootErr(movePhaseCopy, "destination", "cannot be read", err)
	}
	parent := filepath.Dir(dst)
	pinfo, err := os.Stat(parent)
	if err != nil {
		return nil, rootErr(movePhaseCopy, "destination parent", "cannot be read", err)
	}
	if !pinfo.IsDir() {
		return nil, rootErr(movePhaseCopy, "destination parent", "is not a directory", nil)
	}
	sr, err := openRootDir(movePhaseCopy, "source", src)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sr.Close() }()
	if err := ctx.Err(); err != nil {
		return nil, stepErr(movePhaseCopy, "", err)
	}
	if err := os.Mkdir(dst, 0o700); err != nil {
		return nil, rootErr(movePhaseCopy, "destination", "cannot be created", err)
	}
	dr, err := os.OpenRoot(dst)
	if err != nil {
		return nil, rootErr(movePhaseCopy, "destination", "cannot be opened", err)
	}
	defer func() { _ = dr.Close() }()
	m := &Manifest{}
	if err := copyDir(ctx, sr, dr, "", 0o700, m); err != nil {
		return nil, err
	}
	if err := fsync.Dir(parent); err != nil {
		return nil, rootErr(movePhaseCopy, "destination parent", "cannot be synced", err)
	}
	sortManifest(m)
	return m, nil
}

// copyDir copies the entries of the directory rel (already created in dr),
// then sets its mode and fsyncs it.
func copyDir(ctx context.Context, sr, dr *os.Root, rel string, mode fs.FileMode, m *Manifest) error {
	entries, _, err := readDirEntries(sr, rel)
	if err != nil {
		return stepErr(movePhaseCopy, rel, err)
	}
	for _, e := range entries {
		child := joinRel(rel, e.Name())
		if err := ctx.Err(); err != nil {
			return stepErr(movePhaseCopy, child, err)
		}
		switch {
		case e.Type().IsDir():
			info, err := sr.Lstat(osName(child))
			if err != nil {
				return stepErr(movePhaseCopy, child, err)
			}
			perm := info.Mode().Perm()
			if err := dr.Mkdir(osName(child), 0o700); err != nil {
				return stepErr(movePhaseCopy, child, err)
			}
			m.Entries = append(m.Entries, ManifestEntry{Path: child, Dir: true, Mode: perm})
			if err := copyDir(ctx, sr, dr, child, perm, m); err != nil {
				return err
			}
		case e.Type().IsRegular():
			entry, err := copyFile(sr, dr, child)
			if err != nil {
				return stepErr(movePhaseCopy, child, err)
			}
			m.Entries = append(m.Entries, entry)
		default:
			return stepErr(movePhaseCopy, child, &unsupportedEntryError{Kind: entryKind(e.Type())})
		}
	}
	d, err := dr.Open(osName(rel))
	if err != nil {
		return stepErr(movePhaseCopy, rel, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Chmod(mode); err != nil {
		return stepErr(movePhaseCopy, rel, err)
	}
	if err := d.Sync(); err != nil {
		return stepErr(movePhaseCopy, rel, err)
	}
	return nil
}

func copyFile(sr, dr *os.Root, rel string) (ManifestEntry, error) {
	in, err := sr.Open(osName(rel))
	if err != nil {
		return ManifestEntry{}, err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return ManifestEntry{}, err
	}
	if !info.Mode().IsRegular() {
		return ManifestEntry{}, &unsupportedEntryError{Kind: entryKind(info.Mode())}
	}
	perm := info.Mode().Perm()
	out, err := dr.OpenFile(osName(rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ManifestEntry{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if err == nil {
		err = out.Chmod(perm)
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return ManifestEntry{}, err
	}
	e := ManifestEntry{Path: rel, Mode: perm, Size: n}
	copy(e.SHA256[:], h.Sum(nil))
	return e, nil
}

// VerifyCopy builds the manifest of dst and compares it with want entry
// by entry. A difference returns a *MoveStepError with Phase "verify"
// wrapping a *ManifestMismatchError that names the first differing
// root-relative path; entries of want are checked in Path order, then
// entries only dst has.
func VerifyCopy(ctx context.Context, dst string, want *Manifest) error {
	if want == nil {
		return &MoveStepError{Phase: movePhaseVerify, Err: errors.New("nil manifest")}
	}
	got, err := BuildManifest(ctx, dst)
	if err != nil {
		return err
	}
	have := make(map[string]ManifestEntry, len(got.Entries))
	for _, e := range got.Entries {
		have[e.Path] = e
	}
	wanted := slices.Clone(want.Entries)
	slices.SortFunc(wanted, func(a, b ManifestEntry) int { return strings.Compare(a.Path, b.Path) })
	seen := make(map[string]bool, len(wanted))
	for _, w := range wanted {
		seen[w.Path] = true
		g, ok := have[w.Path]
		reason := ""
		switch {
		case !ok:
			reason = "missing"
		case g.Dir != w.Dir:
			reason = "type"
		case g.Mode != w.Mode:
			reason = "mode"
		case g.Size != w.Size:
			reason = "size"
		case g.SHA256 != w.SHA256:
			reason = "digest"
		}
		if reason != "" {
			return mismatch(w.Path, reason)
		}
	}
	for _, g := range got.Entries {
		if !seen[g.Path] {
			return mismatch(g.Path, "extra")
		}
	}
	return nil
}

func mismatch(rel, reason string) error {
	return &MoveStepError{Phase: movePhaseVerify, Path: rel, Err: &ManifestMismatchError{Path: rel, Reason: reason}}
}
