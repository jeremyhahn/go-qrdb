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

package audit

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// ErrOutsideTrailDir is returned when a chain-file trail is named by a path
// that leaves the directory it is opened in. The directory is the trail's
// confinement: nothing of the trail is read, written, renamed or removed
// outside it.
var ErrOutsideTrailDir = errors.New("audit: path leaves the trail's directory")

// trailDir is the directory a chain-file trail is confined to, opened as an
// *os.Root. Every file operation of the trail resolves inside it -- the
// active file's open, a rotation's rename and its archive name, retention's
// listing and removals, and the readers' opens and listings -- so a symbolic
// link placed in the directory, at the trail's name or at a directory on its
// path, cannot carry the trail anywhere else. A link that stays inside is
// followed; one that leaves is refused.
//
// The trail's paths stay what they were, the directory joined with a name
// below it, and are what its errors and verification reports name. Each is
// mapped to its name in the root (name) at the point of use.
type trailDir struct {
	root *os.Root
	base string
}

// openTrailDir opens base, the directory a trail is confined to.
func openTrailDir(base string) (*trailDir, error) {
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, &FileError{Path: base, Err: err}
	}
	return &trailDir{root: root, base: base}, nil
}

// path returns the trail path of name, a name in the root.
func (d *trailDir) path(name string) string {
	return filepath.Join(d.base, name)
}

// name maps path, a trail path, to its name in the root.
func (d *trailDir) name(path string) (string, error) {
	rel, err := filepath.Rel(d.base, path)
	if err != nil || !filepath.IsLocal(rel) && rel != "." {
		return "", &FileError{Path: path, Err: ErrOutsideTrailDir}
	}
	return rel, nil
}

// openFile opens the trail file at path with flag and perm.
func (d *trailDir) openFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	name, err := d.name(path)
	if err != nil {
		return nil, err
	}
	return d.root.OpenFile(name, flag, perm)
}

// open opens the trail file at path for reading.
func (d *trailDir) open(path string) (*os.File, error) {
	return d.openFile(path, os.O_RDONLY, 0)
}

// readDir lists the directory at dir, sorted by name as os.ReadDir is.
func (d *trailDir) readDir(dir string) ([]os.DirEntry, error) {
	f, err := d.open(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

// stat describes the trail file at path.
func (d *trailDir) stat(path string) (os.FileInfo, error) {
	name, err := d.name(path)
	if err != nil {
		return nil, err
	}
	return d.root.Stat(name)
}

// remove removes the trail file at path.
func (d *trailDir) remove(path string) error {
	name, err := d.name(path)
	if err != nil {
		return err
	}
	return d.root.Remove(name)
}

// rename renames the trail file at oldpath to newpath inside the root and
// fsyncs the containing directories, as fsync.Rename does: the new name's,
// and the old name's too when it differs, so both halves of the rename are
// durable when it returns nil. The rename's own error is returned unwrapped.
func (d *trailDir) rename(oldpath, newpath string) error {
	oldName, err := d.name(oldpath)
	if err != nil {
		return err
	}
	newName, err := d.name(newpath)
	if err != nil {
		return err
	}
	if err := d.root.Rename(oldName, newName); err != nil {
		return err
	}
	newDir := filepath.Dir(newpath)
	if err := fsync.Dir(newDir); err != nil {
		return err
	}
	if oldDir := filepath.Dir(oldpath); oldDir != newDir {
		return fsync.Dir(oldDir)
	}
	return nil
}

// close releases the root.
func (d *trailDir) close() error {
	return d.root.Close()
}
