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

package routing

// EmptyShardListError is returned when a routing operation is attempted
// with no shards configured. The default constructors prevent this
// condition, but the error exists for defensive use in future extensions.
type EmptyShardListError struct{}

func (e *EmptyShardListError) Error() string {
	return "routing: shard list is empty"
}
