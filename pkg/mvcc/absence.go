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

package mvcc

import "github.com/jeremyhahn/go-qrdb/pkg/errors"

// absent reports whether a storage.Reader.Get found nothing.
//
// This package used to spell absence as errors.IsNotFound(err) alone. No
// shipped engine produces that: storage.Reader.Get documents "Returns nil, nil
// when key does not exist" and memory, file and pebble all honour it literally.
// So err was nil at every one of those guards, the absence arm never ran,
// execution fell through, and a decoder rejected the empty slice — which is why
// pkg/mvcc could not construct, and then could not Put, over any real engine
// (go-qrdb#24).
//
// Absence is data == nil, NOT len(data) == 0. The two are different states and
// the engines distinguish them: a missing key yields a literal nil slice, while
// a key holding an empty value yields a non-nil zero-length one, because every
// engine allocates on the value path.
//
//	absent       -> nil=true  len=0 err=<nil>
//	stored empty -> nil=false len=0 err=<nil>
//
// Reading it as len(data) == 0 would report a legitimately empty value as
// missing — a silent wrong answer on an MVCC read path, which is worse than the
// loud failure it replaces. The identical conflation was caught in review on
// pkg/dao (go-qrdb#58), and the measurement above is the one taken to confirm
// it, repeated here against pkg/storage's own engines.
//
// The typed-error arm is kept because it is not redundant: a decorator, or a
// Reader implementation outside this repository, may signal absence that way,
// and both spellings must mean the same thing. A read that fails for any other
// reason is NOT absence — err is returned to the caller instead.
func absent(data []byte, err error) bool {
	if err != nil {
		return errors.IsNotFound(err)
	}
	return data == nil
}
