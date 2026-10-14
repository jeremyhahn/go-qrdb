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

// wrapRootKey wraps rootKey under key for domain, binding the given roster, and
// returns the serialized blob with the roster digest. It builds the AEAD from
// key and defers to wrapRootKeyWith, the form production uses once the key is
// held in protected memory. Neither key slice is wiped; the caller owns both.
func (b *StorageBarrier) wrapRootKey(domain QuorumDomain, key, rootKey []byte, roster *HolderRoster) ([]byte, []byte, error) {
	if roster == nil || roster.Domain != domain {
		return nil, nil, &InvalidRosterError{Reason: "roster belongs to another domain"}
	}
	aead, err := b.unsealKeyAEAD(key)
	if err != nil {
		return nil, nil, err
	}
	return b.wrapRootKeyWith(domain, aead, rootKey, roster)
}
