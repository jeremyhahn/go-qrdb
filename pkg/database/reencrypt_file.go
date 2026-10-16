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

package database

import (
	"bytes"
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
)

// ReencryptFileStore copies every record of the file store at srcDir, readable
// under srcEnc, into a new file store at dstDir encrypted under dstEnc, and
// returns the number of records copied. It is the file backend's counterpart
// to ReencryptPebbleStore: the copy goes through the file engine, so every
// file of the new store is written by dstEnc's encrypting file system and its
// format marker records the destination's posture. Every migration mechanism
// recodes a file store this way; ReencryptStoreFiles's file-by-file copy is
// for Pebble's layout.
func ReencryptFileStore(ctx context.Context, srcDir string, srcEnc *atrest.Encryptor, dstDir string, dstEnc *atrest.Encryptor) (count int64, err error) {
	src, dst, err := openFileStorePair(srcDir, srcEnc, dstDir, dstEnc)
	if err != nil {
		return 0, err
	}
	defer func() { _ = src.Close() }()
	defer func() {
		if cerr := dst.Close(); cerr != nil && err == nil {
			err = &ReencryptCopyError{Err: cerr}
		}
	}()

	it := src.NewIterator(nil)
	defer func() { _ = it.Close() }()
	for valid := it.SeekToFirst(); valid; valid = it.Next() {
		if err := ctx.Err(); err != nil {
			return count, &ReencryptCopyError{Err: err}
		}
		if err := dst.Set(it.Key(), it.Value()); err != nil {
			return count, &ReencryptCopyError{Err: err}
		}
		count++
	}
	if err := it.Error(); err != nil {
		return count, &ReencryptCopyError{Err: err}
	}
	return count, nil
}

// VerifyReencryptedFileStore proves that the file store at dstDir, readable
// under dstEnc, holds exactly the records of the one at srcDir, readable under
// srcEnc: the same keys in the same order with the same values. It is the file
// backend's counterpart to VerifyReencryptedStore and returns a
// *ReencryptVerifyMismatchError naming the first difference.
func VerifyReencryptedFileStore(ctx context.Context, srcDir string, srcEnc *atrest.Encryptor, dstDir string, dstEnc *atrest.Encryptor) error {
	src, dst, err := openFileStorePair(srcDir, srcEnc, dstDir, dstEnc)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	defer func() { _ = dst.Close() }()

	sIt := src.NewIterator(nil)
	defer func() { _ = sIt.Close() }()
	dIt := dst.NewIterator(nil)
	defer func() { _ = dIt.Close() }()

	sValid, dValid := sIt.SeekToFirst(), dIt.SeekToFirst()
	for sValid && dValid {
		if err := ctx.Err(); err != nil {
			return &ReencryptCopyError{Err: err}
		}
		if !bytes.Equal(sIt.Key(), dIt.Key()) {
			return &ReencryptVerifyMismatchError{Reason: "key mismatch at " + keyLocator(sIt.Key()) + " vs " + keyLocator(dIt.Key())}
		}
		if !bytes.Equal(sIt.Value(), dIt.Value()) {
			return &ReencryptVerifyMismatchError{Reason: "value mismatch for key " + keyLocator(sIt.Key())}
		}
		sValid, dValid = sIt.Next(), dIt.Next()
	}
	if err := sIt.Error(); err != nil {
		return &ReencryptCopyError{Err: err}
	}
	if err := dIt.Error(); err != nil {
		return &ReencryptCopyError{Err: err}
	}
	if sValid != dValid {
		return &ReencryptVerifyMismatchError{Reason: "record count mismatch (one store has extra keys)"}
	}
	return nil
}

// openFileStorePair opens the file store at srcDir under srcEnc and the one at
// dstDir under dstEnc, creating the latter if it does not exist.
func openFileStorePair(srcDir string, srcEnc *atrest.Encryptor, dstDir string, dstEnc *atrest.Encryptor) (src, dst storage.Storage, err error) {
	src, err = file.New(srcDir, file.WithEncryptor(srcEnc))
	if err != nil {
		return nil, nil, &ReencryptOpenError{Dir: srcDir, Err: err}
	}
	dst, err = file.New(dstDir, file.WithEncryptor(dstEnc))
	if err != nil {
		_ = src.Close()
		return nil, nil, &ReencryptOpenError{Dir: dstDir, Err: err}
	}
	return src, dst, nil
}
