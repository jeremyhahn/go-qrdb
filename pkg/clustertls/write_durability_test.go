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

// go-qrdb#241: atomic visibility and crash durability are different
// properties, and WriteFileAtomic promises both.
//
// WriteFileAtomic's own durability tests
// (TestWriteFileAtomic_DoesNotSucceedUntilTheDirectoryIsSynced,
// TestWriteFileAtomic_SyncsTheDirectoryAfterEverySuccessfulWrite) moved to
// pkg/util/securefile with the function itself (go-qrdb#580). The tests
// below stay here: they pin PersistMaterial and PersistCA, which still live
// in this package and now call through securefile.WriteFileAtomic.
package clustertls

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// TestPersistMaterial_IsCrashDurable records that fixing the shared helper
// fixed callers this change never touched. PersistMaterial and PersistCA have
// always written through WriteFileAtomic, so their material was atomic but not
// crash-durable, before go-qrdb#241 and independently of it.
func TestPersistMaterial_IsCrashDurable(t *testing.T) {
	dir := t.TempDir()
	sentinel := errors.New("directory sync refused")
	t.Cleanup(fsync.SetForTest(func(string) error { return sentinel }))

	mat, err := GenerateCABundle("spiffe://test.cluster/node/127.0.0.1:63001")
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	if err := PersistMaterial(mat, dir); !errors.Is(err, sentinel) {
		t.Errorf("PersistMaterial = %v; want the directory-sync failure propagated. "+
			"TLS material that reports persisted before its directory entries are durable "+
			"can come back partially after a power loss", err)
	}
	if err := PersistCA(mat.CACertPEM, mat.CAKeyPEM, dir); !errors.Is(err, sentinel) {
		t.Errorf("PersistCA = %v; want the directory-sync failure propagated", err)
	}
}
