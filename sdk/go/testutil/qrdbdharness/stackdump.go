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

package qrdbdharness

import (
	"io"
	"net/url"
	"strings"
	"sync"
	"time"
)

// stackDumpDrain bounds how long a timed-out wait holds the test open for a
// signalled daemon's goroutine dump to reach its output writer. A Go process
// writes the dump and exits within milliseconds; the bound only matters when
// the process ignores the signal.
const stackDumpDrain = 10 * time.Second

// spawned holds every daemon Start launched that has not yet been seen to
// exit, keyed by *Daemon. A daemon whose process is gone is pruned the next
// time it is looked up, and Daemon.reap prunes it as soon as it is reaped.
var spawned sync.Map

// outputWriter passes a daemon's combined stdout and stderr through to its
// output tail and the writer the caller supplied, and reports when that
// stream has ended.
//
// exec.Cmd copies a non-*os.File Stdout through io.Copy, which hands the
// pipe to an io.ReaderFrom. ReadFrom therefore returns, and drained closes,
// only when the pipe reaches EOF: the process has exited and every byte it
// wrote, a goroutine dump included, has been passed on. Start sets the same
// *outputWriter as both Stdout and Stderr, so exec shares one pipe and one
// copy between them.
type outputWriter struct {
	w       io.Writer
	once    sync.Once
	drained chan struct{}
}

func newOutputWriter(w io.Writer) *outputWriter {
	return &outputWriter{w: w, drained: make(chan struct{})}
}

// Write passes p through unchanged.
func (o *outputWriter) Write(p []byte) (int, error) { return o.w.Write(p) }

// ReadFrom copies r to the underlying writer until EOF, then marks the
// stream drained.
func (o *outputWriter) ReadFrom(r io.Reader) (int64, error) {
	defer o.once.Do(func() { close(o.drained) })
	// The anonymous struct hides o's own ReadFrom, so io.Copy uses Write.
	return io.Copy(struct{ io.Writer }{o.w}, r)
}

// dumpStacks sends stackDumpSignal to every live daemon this package spawned
// whose arguments name the port in rawURL, so a wait that timed out on a node
// leaves that node's goroutine dump in its output -- the test log, for a
// daemon started WithOutput. It waits, up to stackDumpDrain, for each
// signalled daemon's output to drain, so the dump is written before the
// caller fails the test. It returns the process IDs it signalled.
//
// The Go runtime answers SIGQUIT by writing every goroutine's stack to
// stderr and exiting, so a signalled daemon is gone afterwards. Every caller
// of WaitForHTTPOK treats a timeout as fatal, which is what makes that safe.
func dumpStacks(rawURL string) []int {
	if stackDumpSignal == nil {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Port() == "" {
		return nil
	}
	port := parsed.Port()

	var pids []int
	var drains []<-chan struct{}
	spawned.Range(func(key, _ any) bool {
		d, ok := key.(*Daemon)
		if !ok || !namesPort(d.args, port) {
			return true
		}
		if err := d.cmd.Process.Signal(stackDumpSignal); err != nil {
			if isProcessDone(err) {
				spawned.Delete(d)
			}
			return true
		}
		pids = append(pids, d.cmd.Process.Pid)
		drains = append(drains, d.out.drained)
		return true
	})

	deadline := time.After(stackDumpDrain)
	for _, drained := range drains {
		select {
		case <-drained:
		case <-deadline:
			return pids
		}
	}
	return pids
}

// namesPort reports whether any argument carries port as a value: the whole
// argument, the value of a --flag=value pair, or an element of a
// comma-separated list, either bare or as the port of a host:port address.
func namesPort(args []string, port string) bool {
	for _, arg := range args {
		for _, field := range strings.FieldsFunc(arg, func(r rune) bool { return r == '=' || r == ',' }) {
			if field == port || strings.HasSuffix(field, ":"+port) {
				return true
			}
		}
	}
	return false
}
