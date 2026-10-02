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

package embedded

import (
	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// delayed returns err as a *transport.RequestError carrying the delay it
// names (errclass.RetryAfter: a throttled request, an SO PIN lockout), so an
// in-process caller reads the delay from RequestError.RetryAfter exactly as a
// network caller does. The RequestError unwraps to err, so errors.As and
// errors.Is against the refusal's own type keep working. An error that names
// no delay -- nil included -- is returned unchanged.
func delayed(method string, err error) error {
	d, ok := errclass.RetryAfter(err)
	if !ok {
		return err
	}
	return &transport.RequestError{Method: method, Err: err, RetryAfter: d}
}
