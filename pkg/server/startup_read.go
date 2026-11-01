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
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// StartupReadAttemptTimeout bounds one attempt of a startup read. It is well
// past a ReadIndex round trip and a leader election under the default raft
// timings, and short enough that every startup budget holds several attempts.
// An attempt that outlives it -- a ReadIndex handed to a leader that died, or
// one quicraft never answers -- is abandoned and retried, not waited on for
// the rest of the boot: a startup read whose only bound is the budget checked
// between attempts hangs the boot on its first attempt that does not return.
const StartupReadAttemptTimeout = 5 * time.Second

// StartupWaitReportInterval paces the progress lines a startup wait logs
// while it retries, so a boot that has not finished says what it is waiting
// for rather than going quiet until its deadline.
const StartupWaitReportInterval = 5 * time.Second

// StartupRead configures RunStartupRead for one boot-time read.
type StartupRead struct {
	// Op names the read in its error and in the boot-failure log line.
	Op string
	// Budget is the startup deadline, measured from the first attempt over
	// the time Counts counts. A budget of zero still makes one attempt.
	Budget time.Duration
	// AttemptTimeout bounds each attempt; zero means
	// StartupReadAttemptTimeout. No attempt runs past the startup deadline.
	AttemptTimeout time.Duration
	// Delay paces the attempts. It doubles after each one up to MaxDelay, and
	// stays constant when MaxDelay is not above it.
	Delay, MaxDelay time.Duration
	// Retryable reports a transient this read waits out beyond the ones every
	// startup read does: quicraft.ErrReadIndexNotReady and an attempt that
	// hit its bound. Nil adds none.
	Retryable func(error) bool
	// Counts reports whether the wait counts against Budget at this moment.
	// It is asked before the first attempt and after each attempt that
	// failed with a transient; the stretch between two asks counts only when
	// both say so, and the deadline moves out by every other stretch. Nil
	// counts all of the wait.
	Counts func(context.Context) bool
	// ReportInterval paces the progress lines of a read still retrying; zero
	// means StartupWaitReportInterval.
	ReportInterval time.Duration
	// Logger records the progress lines and the boot failure; nil means
	// slog.Default().
	Logger *slog.Logger
}

// RunStartupRead runs read until it succeeds, fails with an error that is
// not transient, ctx ends, or the startup deadline (r.Budget) passes. Each
// attempt gets its own context, derived from ctx and bounded by
// r.AttemptTimeout and by the deadline (StartupReadAttemptContext); read must
// honour it. Transient are go-quicraft's typed retryable
// quicraft.ErrReadIndexNotReady -- returned while a freshly elected leader has
// not committed an entry in its term -- an attempt that hit its bound, and
// whatever r.Retryable reports.
//
// A non-transient error is returned as read returned it, with read's value:
// a read that reports partial work alongside its error keeps it. A deadline that
// passes on a transient is logged and returned as *StartupReadError, which
// unwraps to the last attempt's error, and so does ctx ending. The first
// attempt is made even on a ctx that has already ended, so the error carries
// what the read said, not only that the caller gave up.
func RunStartupRead[T any](ctx context.Context, r StartupRead, read func(context.Context) (T, error)) (T, error) {
	var zero T
	timeout := r.AttemptTimeout
	if timeout <= 0 {
		timeout = StartupReadAttemptTimeout
	}
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}
	report := r.ReportInterval
	if report <= 0 {
		report = StartupWaitReportInterval
	}
	start := time.Now()
	giveUpAt := start.Add(r.Budget)
	counted, counting := start, r.Counts == nil || r.Counts(ctx)
	failure := &StartupReadError{Op: r.Op, Budget: r.Budget, AttemptTimeout: timeout}
	var progress atomic.Pointer[StartupReadError]
	defer reportStartupRead(logger, r.Op, r.Budget, report, start, &progress)()
	delay := r.Delay
	for {
		actx, cancel := StartupReadAttemptContext(ctx, giveUpAt, timeout)
		v, err := read(actx)
		// The attempt's own bound, not ctx, ended it: ctx is checked first
		// because a ctx that ended also ends actx.
		timedOut := err != nil && ctx.Err() == nil && errors.Is(actx.Err(), context.DeadlineExceeded)
		cancel()
		if err == nil {
			return v, nil
		}
		failure.Attempts++
		failure.Err = err
		if timedOut {
			failure.TimedOut++
		}
		failure.Elapsed = time.Since(start)
		if cerr := ctx.Err(); cerr != nil {
			failure.Cause = cerr
			return zero, failure
		}
		if !timedOut && !startupReadTransient(r, err) {
			return v, err
		}
		now := time.Now()
		if r.Counts != nil {
			was := counting
			if counting = r.Counts(ctx); !was || !counting {
				// The probe's own time does not count either.
				now = time.Now()
				giveUpAt = giveUpAt.Add(now.Sub(counted))
			}
		}
		counted = now
		if !now.Before(giveUpAt) {
			logger.Error("startup read did not succeed before its deadline",
				slog.String("op", r.Op),
				slog.Int("attempts", failure.Attempts),
				slog.Int("attempts_timed_out", failure.TimedOut),
				slog.Duration("elapsed", failure.Elapsed),
				slog.Duration("budget", r.Budget),
				slog.Duration("attempt_timeout", timeout),
				slog.Any("error", err))
			return zero, failure
		}
		snapshot := *failure
		progress.Store(&snapshot)
		select {
		case <-ctx.Done():
			failure.Cause = ctx.Err()
			failure.Elapsed = time.Since(start)
			return zero, failure
		case <-time.After(delay):
		}
		if delay < r.MaxDelay {
			delay = min(2*delay, r.MaxDelay)
		}
	}
}

// reportStartupRead logs a startup read's progress every interval until the
// returned stop is called, whether an attempt is in flight or the read is
// between attempts: a read that ignores its attempt's context outlasts the
// bound, and the boot must still say what it waits for. last holds the most
// recent failed attempt's snapshot, nil before the first one fails.
func reportStartupRead(logger *slog.Logger, op string, budget, interval time.Duration, start time.Time, last *atomic.Pointer[StartupReadError]) (stop func()) {
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}
			attrs := []any{slog.String("op", op), slog.Duration("elapsed", time.Since(start)), slog.Duration("budget", budget)}
			if s := last.Load(); s != nil {
				attrs = append(attrs, slog.Int("attempts", s.Attempts), slog.Int("attempts_timed_out", s.TimedOut), slog.Any("error", s.Err))
			}
			logger.Warn("startup read still waiting", attrs...)
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// StartupReadAttemptContext derives one startup read attempt's context from
// ctx: it ends with ctx, after timeout, or at giveUpAt (the startup
// deadline), whichever comes first. An attempt made at or past giveUpAt -- the
// first attempt of a zero budget -- gets the whole timeout.
func StartupReadAttemptContext(ctx context.Context, giveUpAt time.Time, timeout time.Duration) (context.Context, context.CancelFunc) {
	if left := time.Until(giveUpAt); left > 0 && left < timeout {
		timeout = left
	}
	return context.WithTimeout(ctx, timeout)
}

// startupReadTransient reports whether err is a transient r waits out.
func startupReadTransient(r StartupRead, err error) bool {
	if errors.Is(err, quicraft.ErrReadIndexNotReady) {
		return true
	}
	return r.Retryable != nil && r.Retryable(err)
}

// StartupReadError reports a startup read that did not succeed: its deadline
// passed on a transient, or its caller's context ended. It is a boot failure
// with its measurement: the attempts made, how many hit their per-attempt
// bound, and the time spent. Unwrap yields the last attempt's error and, when
// the caller gave up, the context's error, so errors.Is and errors.As see
// both, and a caller classifying the failure sees what the read last said.
type StartupReadError struct {
	// Op names the read.
	Op string
	// Budget is the startup deadline the read had.
	Budget time.Duration
	// AttemptTimeout is the bound on each attempt.
	AttemptTimeout time.Duration
	// Attempts counts the attempts made; TimedOut those that hit their bound.
	Attempts, TimedOut int
	// Elapsed is the time from the first attempt to giving up.
	Elapsed time.Duration
	// Err is the last attempt's error.
	Err error
	// Cause is the caller's context error when the caller gave up before
	// the deadline; nil when the deadline passed.
	Cause error
}

// Error implements the error interface.
func (e *StartupReadError) Error() string {
	var b strings.Builder
	b.WriteString("server: startup read ")
	b.WriteString(e.Op)
	if e.Cause != nil {
		b.WriteString(" abandoned (")
		b.WriteString(e.Cause.Error())
		b.WriteString(") after ")
	} else {
		b.WriteString(" did not succeed within its ")
		b.WriteString(e.Budget.String())
		b.WriteString(" budget: ")
	}
	b.WriteString(strconv.Itoa(e.Attempts))
	b.WriteString(" attempts in ")
	b.WriteString(e.Elapsed.Round(time.Millisecond).String())
	if e.TimedOut > 0 {
		b.WriteString(", ")
		b.WriteString(strconv.Itoa(e.TimedOut))
		b.WriteString(" of them cut off at the ")
		b.WriteString(e.AttemptTimeout.String())
		b.WriteString(" per-attempt bound")
	}
	if e.Err != nil {
		b.WriteString("; last attempt: ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap returns the last attempt's error and the caller's context error.
func (e *StartupReadError) Unwrap() []error {
	errs := make([]error, 0, 2)
	if e.Err != nil {
		errs = append(errs, e.Err)
	}
	if e.Cause != nil {
		errs = append(errs, e.Cause)
	}
	return errs
}
