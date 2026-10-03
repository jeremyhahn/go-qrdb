// Copyright 2026 Jeremy Hahn
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

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeroenrinzema/psql-wire/codes"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"
)

// TestStatementTimeoutError: only a failure whose statement context expired
// becomes 57014; success, another failure, and a cancellation that is not the
// timeout pass through unchanged. With no timeout the statement context never
// expires.
func TestStatementTimeoutError(t *testing.T) {
	h := &ParseHandler{queryTimeout: time.Millisecond}
	expired, cancel := h.statementContext(context.Background())
	defer cancel()
	<-expired.Done()
	boom := errors.New("boom")

	if err := h.statementTimeoutError(expired, nil); err != nil {
		t.Fatalf("a statement that finished: %v", err)
	}
	err := h.statementTimeoutError(expired, context.Canceled)
	var timeout *QueryTimeoutError
	if !errors.As(err, &timeout) || psqlerr.Flatten(err).Code != codes.QueryCanceled {
		t.Fatalf("an expired statement: %v, want QueryTimeoutError as 57014", err)
	}
	if timeout.Error() == "" {
		t.Fatal("empty QueryTimeoutError message")
	}

	cancelled, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if err := h.statementTimeoutError(cancelled, boom); !errors.Is(err, boom) {
		t.Fatalf("a cancellation that is not the timeout: %v, want it unchanged", err)
	}

	unbounded, cancel3 := (&ParseHandler{}).statementContext(context.Background())
	defer cancel3()
	if _, ok := unbounded.Deadline(); ok {
		t.Fatal("a zero query timeout set a deadline")
	}
}
