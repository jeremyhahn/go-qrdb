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

package seal

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/jeremyhahn/go-qrdb/pkg/validation"
)

// DirectoryRemover is the filesystem surface the registry reclaims a tenant's
// directories through. Production uses the operating system; it is an option
// (WithDirectoryRemover) so a reclamation's failure handling -- a removal that
// fails, a DataRoot that is a mount point -- can be proven without a real
// failing disk or a real mount.
type DirectoryRemover interface {
	// ReadDir lists a directory's entries (os.ReadDir semantics).
	ReadDir(path string) ([]os.DirEntry, error)
	// RemoveAll removes a path and everything under it (os.RemoveAll).
	RemoveAll(path string) error
	// Remove removes one file or EMPTY directory (os.Remove).
	Remove(path string) error
}

// osDirectoryRemover is the operating-system DirectoryRemover.
type osDirectoryRemover struct{}

func (osDirectoryRemover) ReadDir(path string) ([]os.DirEntry, error) { return os.ReadDir(path) }
func (osDirectoryRemover) RemoveAll(path string) error                { return os.RemoveAll(path) }
func (osDirectoryRemover) Remove(path string) error                   { return os.Remove(path) }

// WithDirectoryRemover replaces the filesystem the registry removes tenant
// directories through. A nil remover is ignored.
func WithDirectoryRemover(d DirectoryRemover) Option {
	return func(r *BarrierRegistry) {
		if d != nil {
			r.remover = d
		}
	}
}

// directoryRemover returns the configured remover, the operating system's by
// default.
func (r *BarrierRegistry) directoryRemover() DirectoryRemover {
	if r.remover != nil {
		return r.remover
	}
	return osDirectoryRemover{}
}

// RemoveDataRoot reclaims a tenant directory: it removes everything INSIDE
// root, then root itself.
//
// Contents first, because a dedicated DataRoot is often a mount point -- an
// operator's disk for one tenant -- and a mount point cannot itself be
// removed: rmdir reports EBUSY (or EXDEV on some filesystems) however empty it
// is. os.RemoveAll fails on exactly that final step, and a reclamation that
// keeps its records until removal succeeds would then retry it forever. So a
// root that has been EMPTIED and refuses its own removal with EBUSY or EXDEV
// counts as reclaimed: nothing of the tenant is left on it, and the mount is
// the operator's to unmount. Any other failure is reported.
//
// An absent root is already reclaimed. A root that is not a directory is
// removed as the path it is.
//
// Only a directory strictly inside one of this node's own volume-pool paths
// is ever removed: anything else is refused with
// *DataRootOutsideLocalRootsError before the filesystem is touched, so a
// record naming another node's path -- or any path a caller was handed --
// can never delete it.
func (r *BarrierRegistry) RemoveDataRoot(ctx context.Context, root string) error {
	if err := r.RequireInsideLocalPool(ctx, root); err != nil {
		return err
	}
	d := r.directoryRemover()
	entries, err := d.ReadDir(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, syscall.ENOTDIR):
		return d.RemoveAll(root)
	case err != nil:
		return err
	}
	for _, e := range entries {
		if err := d.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			return err
		}
	}
	err = d.Remove(root)
	switch {
	case err == nil, errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, syscall.EBUSY), errors.Is(err, syscall.EXDEV):
		if r.logger != nil {
			r.logger.Info("registry: tenant data root emptied; it is a mount point and was left for the operator to unmount",
				slog.String("data_root", root),
				slog.Any("rmdir_error", err))
		}
		return nil
	default:
		return err
	}
}

// RequireInsideLocalPool returns *DataRootOutsideLocalRootsError unless path
// is strictly inside one of this node's own volume-pool paths.
//
// The path must be absolute and already clean -- a ".." component, a
// trailing separator or a doubled one is refused rather than normalised --
// and is compared twice, both of which must pass:
//
//   - as given, against each cleaned pool path, component by component, so
//     /pool10/t is not inside /pool1 and a pool path itself is not inside
//     itself;
//   - with every symbolic link resolved on both sides, so a link inside the
//     pool -- a tenant directory, or any directory above it -- that points
//     outside cannot carry a removal there. A path that does not exist yet is
//     resolved through its deepest existing ancestor.
//
// A pool path that cannot be resolved contains nothing; a path whose
// resolution fails for any reason other than not existing is refused.
func (r *BarrierRegistry) RequireInsideLocalPool(ctx context.Context, path string) error {
	bases, err := r.PoolBasePaths(ctx)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return &DataRootOutsideLocalRootsError{DataRoot: path}
	}
	resolved, err := resolveExisting(path)
	if err != nil {
		return &DataRootOutsideLocalRootsError{DataRoot: path, Err: err}
	}
	for _, base := range bases {
		cleanBase := filepath.Clean(base)
		if !validation.IsPathAncestor(cleanBase, path) {
			continue
		}
		resolvedBase, berr := filepath.EvalSymlinks(cleanBase)
		if berr != nil {
			continue
		}
		if validation.IsPathAncestor(resolvedBase, resolved) {
			return nil
		}
	}
	return &DataRootOutsideLocalRootsError{DataRoot: path}
}

// resolveExisting returns path with every symbolic link resolved. A path that
// does not exist is resolved through its deepest existing ancestor, with the
// missing components appended as given.
func resolveExisting(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path, nil
	}
	resolvedParent, err := resolveExisting(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(path)), nil
}
