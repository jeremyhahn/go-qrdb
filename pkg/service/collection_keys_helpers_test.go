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

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every collection created by CreateCollection keeps its documents and its ID
// sequence in keyspaces named by its incarnation, which is random. A test that
// needs a raw key reads the incarnation back from the stored record rather
// than spelling the key out.

// storedCollectionForTest returns the stored record of database/name, in
// whatever lifecycle state it is in.
func storedCollectionForTest(t *testing.T, svc *CollectionService, database, name string) *CollectionInfo {
	t.Helper()
	info, err := svc.loadCollectionInfo(context.Background(), database, name)
	require.NoError(t, err)
	return info
}

// docKeyForTest returns the storage key of document id in the current
// incarnation of database/name.
func docKeyForTest(t *testing.T, svc *CollectionService, database, name string, id uint64) string {
	t.Helper()
	return collectionDocumentKey(storedCollectionForTest(t, svc, database, name), id)
}

// docPrefixForTest returns the document key prefix of the current
// incarnation of database/name.
func docPrefixForTest(t *testing.T, svc *CollectionService, database, name string) string {
	t.Helper()
	return storedCollectionForTest(t, svc, database, name).documentPrefix()
}

// seqNameForTest returns the ID sequence name of the current incarnation of
// database/name.
func seqNameForTest(t *testing.T, svc *CollectionService, database, name string) string {
	t.Helper()
	return collectionSequenceName(storedCollectionForTest(t, svc, database, name))
}
