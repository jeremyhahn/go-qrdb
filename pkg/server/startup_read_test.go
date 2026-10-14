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

package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// syncBuffer is a bytes.Buffer a slog handler and the test can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogger returns a logger writing text records into the returned buffer.
func captureLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buf, nil)), buf
}

// A read that never returns -- it waits on its context and nothing else, as
// a ReadIndex handed to a dead leader does -- is cut off at the per-attempt
// bound and retried, and when the startup deadline passes the boot fails with
// a typed, logged *StartupReadError instead of hanging.
func TestRunStartupRead_ReadThatNeverReturnsFailsTheBoot(t *testing.T) {
	logger, logs := captureLogger()
	calls := 0
	start := time.Now()
	_, err := RunStartupRead(context.Background(), StartupRead{
		Op:             "hung read",
		Budget:         200 * time.Millisecond,
		AttemptTimeout: 40 * time.Millisecond,
		Delay:          time.Millisecond,
		Logger:         logger,
	}, func(actx context.Context) (int, error) {
		calls++
		<-actx.Done()
		return 0, actx.Err()
	})
	elapsed := time.Since(start)

	var sre *StartupReadError
	if !errors.As(err, &sre) {
		t.Fatalf("err = %v (%T), want *StartupReadError", err, err)
	}
	if sre.Cause != nil {
		t.Errorf("Cause = %v, want nil: the deadline passed, the caller did not give up", sre.Cause)
	}
	if calls < 2 || sre.Attempts != calls || sre.TimedOut != calls {
		t.Errorf("calls=%d Attempts=%d TimedOut=%d, want every one of >=2 attempts cut off at its bound", calls, sre.Attempts, sre.TimedOut)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to unwrap to the attempt's context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %s, want the 200ms budget honoured", elapsed)
	}
	if got := logs.String(); !strings.Contains(got, "level=ERROR") || !strings.Contains(got, "op=\"hung read\"") {
		t.Errorf("boot failure not logged at ERROR with its op; log = %q", got)
	}
}

// One attempt that hangs costs that attempt only: the next one is made and
// its answer returned.
func TestRunStartupRead_HungAttemptIsRetried(t *testing.T) {
	calls := 0
	got, err := RunStartupRead(context.Background(), StartupRead{
		Op:             "flaky read",
		Budget:         5 * time.Second,
		AttemptTimeout: 30 * time.Millisecond,
		Delay:          time.Millisecond,
	}, func(actx context.Context) (string, error) {
		calls++
		if calls == 1 {
			<-actx.Done()
			return "", actx.Err()
		}
		return "ok", nil
	})
	if err != nil || got != "ok" || calls != 2 {
		t.Fatalf("got %q, %v after %d calls; want \"ok\" after 2", got, err, calls)
	}
}

// go-quicraft's typed retryable ErrReadIndexNotReady is retried until the
// read succeeds, and the read's value is returned.
func TestRunStartupRead_RetriesReadIndexNotReadyThenSucceeds(t *testing.T) {
	const notReady = 4
	calls := 0
	got, err := RunStartupRead(context.Background(), StartupRead{
		Op:     "warm-up read",
		Budget: 5 * time.Second,
		Delay:  time.Millisecond,
	}, func(context.Context) (int, error) {
		calls++
		if calls <= notReady {
			return 0, quicraft.ErrReadIndexNotReady
		}
		return 42, nil
	})
	if err != nil || got != 42 || calls != notReady+1 {
		t.Fatalf("got %d, %v after %d calls; want 42 after %d", got, err, calls, notReady+1)
	}
}

// ErrReadIndexNotReady outlasting the startup deadline is the typed boot
// failure, logged, and still matches ErrReadIndexNotReady so a caller can
// classify it.
func TestRunStartupRead_DeadlineExhaustedOnReadIndexNotReady(t *testing.T) {
	logger, logs := captureLogger()
	calls := 0
	_, err := RunStartupRead(context.Background(), StartupRead{
		Op:     "never ready",
		Budget: 30 * time.Millisecond,
		Delay:  2 * time.Millisecond,
		Logger: logger,
	}, func(context.Context) (int, error) {
		calls++
		return 0, quicraft.ErrReadIndexNotReady
	})
	var sre *StartupReadError
	if !errors.As(err, &sre) || !errors.Is(err, quicraft.ErrReadIndexNotReady) {
		t.Fatalf("err = %v (%T), want *StartupReadError unwrapping to ErrReadIndexNotReady", err, err)
	}
	if calls < 2 || sre.Attempts != calls || sre.TimedOut != 0 {
		t.Errorf("calls=%d Attempts=%d TimedOut=%d, want several attempts, none cut off", calls, sre.Attempts, sre.TimedOut)
	}
	if sre.Budget != 30*time.Millisecond || sre.AttemptTimeout != StartupReadAttemptTimeout || sre.Op != "never ready" {
		t.Errorf("error fields = %+v, want the budget, the default attempt bound and the op", sre)
	}
	if !strings.Contains(logs.String(), "never ready") {
		t.Errorf("boot failure not logged; log = %q", logs.String())
	}
}

// A zero budget still makes the one attempt, with the whole attempt bound.
func TestRunStartupRead_ZeroBudgetMakesOneAttempt(t *testing.T) {
	logger, _ := captureLogger()
	calls := 0
	var left time.Duration
	_, err := RunStartupRead(context.Background(), StartupRead{Op: "once", Logger: logger}, func(actx context.Context) (int, error) {
		calls++
		dl, _ := actx.Deadline()
		left = time.Until(dl)
		return 0, quicraft.ErrReadIndexNotReady
	})
	var sre *StartupReadError
	if !errors.As(err, &sre) || calls != 1 {
		t.Fatalf("err = %v after %d calls, want *StartupReadError after 1", err, calls)
	}
	if left < StartupReadAttemptTimeout-time.Second {
		t.Errorf("attempt had %s, want the whole %s bound", left, StartupReadAttemptTimeout)
	}
}

// Anything that is not a transient is returned at once, as the read
// returned it, and is not logged as a startup-deadline failure.
func TestRunStartupRead_PermanentErrorReturnedAsIs(t *testing.T) {
	logger, logs := captureLogger()
	boom := errors.New("boom")
	calls := 0
	got, err := RunStartupRead(context.Background(), StartupRead{
		Op: "permanent", Budget: time.Minute, Delay: time.Second, Logger: logger,
	}, func(context.Context) (int, error) {
		calls++
		return 3, boom
	})
	if err != boom || calls != 1 {
		t.Fatalf("err = %v (%T) after %d calls, want boom itself after 1", err, err, calls)
	}
	if got != 3 {
		t.Errorf("value = %d, want the partial result the read returned with its error", got)
	}
	if logs.String() != "" {
		t.Errorf("a permanent error was logged as a deadline failure: %q", logs.String())
	}
}

// Retryable adds the caller's own transients to the ones every startup read
// waits out.
func TestRunStartupRead_RetryablePredicate(t *testing.T) {
	settling := errors.New("settling")
	calls := 0
	got, err := RunStartupRead(context.Background(), StartupRead{
		Op:        "settling read",
		Budget:    5 * time.Second,
		Delay:     time.Millisecond,
		MaxDelay:  4 * time.Millisecond,
		Retryable: func(err error) bool { return errors.Is(err, settling) },
	}, func(context.Context) (int, error) {
		calls++
		if calls < 5 {
			return 0, settling
		}
		return 7, nil
	})
	if err != nil || got != 7 || calls != 5 {
		t.Fatalf("got %d, %v after %d calls; want 7 after 5", got, err, calls)
	}
}

// A context that has already ended still gets the one attempt, and one that
// ends during the backoff stops the retries; either way the error carries
// both the caller's reason and the read's last answer.
func TestRunStartupRead_ContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := RunStartupRead(ctx, StartupRead{Op: "cancelled", Budget: time.Minute}, func(context.Context) (int, error) {
		calls++
		return 0, quicraft.ErrReadIndexNotReady
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, quicraft.ErrReadIndexNotReady) || calls != 1 {
		t.Fatalf("err = %v after %d calls, want context.Canceled and the read's answer after 1", err, calls)
	}

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err = RunStartupRead(ctx, StartupRead{Op: "abandoned", Budget: time.Minute, Delay: time.Minute}, func(context.Context) (int, error) {
		return 0, quicraft.ErrReadIndexNotReady
	})
	var sre *StartupReadError
	if !errors.As(err, &sre) || !errors.Is(err, context.Canceled) || !errors.Is(err, quicraft.ErrReadIndexNotReady) {
		t.Fatalf("err = %v (%T), want *StartupReadError matching context.Canceled and ErrReadIndexNotReady", err, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %s, want an immediate return once ctx was cancelled", time.Since(start))
	}
}

// An attempt in flight when the caller gives up ends with it, and is not
// counted as cut off by its own bound.
func TestRunStartupRead_ContextEndsMidAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := RunStartupRead(ctx, StartupRead{Op: "in flight", Budget: time.Minute}, func(actx context.Context) (int, error) {
		<-actx.Done()
		return 0, actx.Err()
	})
	var sre *StartupReadError
	if !errors.As(err, &sre) || sre.Cause == nil || sre.TimedOut != 0 || sre.Attempts != 1 {
		t.Fatalf("err = %#v, want one attempt ended by the caller, none by its bound", err)
	}
}

// The attempt context ends at the earliest of its bound and the startup
// deadline, and with its parent.
func TestStartupReadAttemptContext(t *testing.T) {
	giveUpAt := time.Now().Add(50 * time.Millisecond)
	actx, cancel := StartupReadAttemptContext(context.Background(), giveUpAt, time.Hour)
	dl, ok := actx.Deadline()
	cancel()
	if !ok || dl.After(giveUpAt.Add(time.Millisecond)) {
		t.Errorf("deadline %v (set=%v), want no later than the startup deadline %v", dl, ok, giveUpAt)
	}

	actx, cancel = StartupReadAttemptContext(context.Background(), time.Now().Add(time.Hour), 50*time.Millisecond)
	dl, _ = actx.Deadline()
	cancel()
	if left := time.Until(dl); left > 50*time.Millisecond {
		t.Errorf("%s left, want the 50ms attempt bound", left)
	}

	actx, cancel = StartupReadAttemptContext(context.Background(), time.Now().Add(-time.Second), 50*time.Millisecond)
	dl, _ = actx.Deadline()
	cancel()
	if left := time.Until(dl); left <= 0 {
		t.Errorf("an attempt past the startup deadline got %s, want the whole bound", left)
	}

	parent, cancelParent := context.WithCancel(context.Background())
	actx, cancel = StartupReadAttemptContext(parent, time.Now().Add(time.Hour), time.Hour)
	defer cancel()
	cancelParent()
	if !errors.Is(actx.Err(), context.Canceled) {
		t.Errorf("attempt context err = %v, want it to end with its parent", actx.Err())
	}
}

// The error names the read, its budget and what the attempts did, and says
// whether the deadline passed or the caller gave up.
func TestStartupReadError_Error(t *testing.T) {
	deadline := &StartupReadError{
		Op: "cluster CA certificate", Budget: 30 * time.Second, AttemptTimeout: 5 * time.Second,
		Attempts: 6, TimedOut: 2, Elapsed: 31 * time.Second, Err: quicraft.ErrReadIndexNotReady,
	}
	msg := deadline.Error()
	for _, want := range []string{"cluster CA certificate", "30s budget", "6 attempts", "2 of them cut off at the 5s per-attempt bound", quicraft.ErrReadIndexNotReady.Error()} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
	if errs := deadline.Unwrap(); len(errs) != 1 || errs[0] != quicraft.ErrReadIndexNotReady {
		t.Errorf("Unwrap() = %v, want only the last attempt's error", errs)
	}

	abandoned := &StartupReadError{Op: "tenant list", Attempts: 1, Cause: context.Canceled}
	msg = abandoned.Error()
	if !strings.Contains(msg, "abandoned (context canceled)") || strings.Contains(msg, "per-attempt") || strings.Contains(msg, "last attempt") {
		t.Errorf("Error() = %q, want the abandonment and nothing about bounds or a missing last error", msg)
	}
	if errs := abandoned.Unwrap(); len(errs) != 1 || errs[0] != context.Canceled {
		t.Errorf("Unwrap() = %v, want only the context error", errs)
	}
}
