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

package sdk

import "github.com/jeremyhahn/go-qrdb/sdk/go/transport"

// RequestError is returned when a request fails on any transport. Its
// RetryAfter is how long the server said to wait before a retry can succeed
// -- a throttled request, an SO PIN lockout, a rate-limited caller -- and
// zero when it named no delay; every transport, embedded included, fills it:
//
//	var reqErr *sdk.RequestError
//	if errors.As(err, &reqErr) && reqErr.RetryAfter > 0 {
//		time.Sleep(reqErr.RetryAfter)
//	}
//
// (Aliases sdk/go/transport.RequestError.)
type RequestError = transport.RequestError
