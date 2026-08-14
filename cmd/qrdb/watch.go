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

package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func eventTypeString(et transport.EventType) string {
	eventTypeNames := map[transport.EventType]string{
		transport.EventPut:    "PUT",
		transport.EventDelete: "DELETE",
	}
	if name, ok := eventTypeNames[et]; ok {
		return name
	}
	return fmt.Sprintf("UNKNOWN(%d)", et)
}

func newWatchCmd() *cobra.Command {
	var (
		prefix        bool
		startRevision int64
		prevKV        bool
	)

	cmd := &cobra.Command{
		Use:   "watch <key>",
		Short: "Watch for changes to a key or prefix",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			ctx := cmd.Context()

			// Bound the watch by the persistent --timeout flag (default 10s,
			// same as every other command's request bound), so `qrdb watch`
			// does not hang forever by default; pass a larger --timeout (or
			// Ctrl-C) to watch longer. A non-positive value disables the
			// bound entirely.
			if timeout, _ := cmd.Root().PersistentFlags().GetDuration("timeout"); timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}

			opts := transport.WatchOptions{
				StartRevision: startRevision,
				PrevKV:        prevKV,
			}

			var (
				ch  <-chan transport.WatchResponse
				err error
			)

			watcher := client.Watch()

			if prefix {
				ch, err = watcher.WatchPrefix(ctx, key, opts)
			} else {
				ch, err = watcher.Watch(ctx, key, opts)
			}
			if err != nil {
				return err
			}

			for {
				select {
				case <-ctx.Done():
					// A timeout or Ctrl-C (SIGINT/SIGTERM, wired via
					// signal.NotifyContext in main.go) ending the watch is
					// expected, graceful termination, not a command failure —
					// exit 0 rather than propagating ctx.Err().
					return nil
				case resp, ok := <-ch:
					if !ok {
						return nil
					}

					if resp.Canceled {
						_, _ = fmt.Fprintf(outputWriter, "watch canceled: %s\n", resp.CancelReason)
						return nil
					}

					if outputFormat == "json" {
						if err := printJSON(resp); err != nil {
							return err
						}
						continue
					}

					for _, event := range resp.Events {
						if event.PrevKv != nil {
							_, _ = fmt.Fprintf(outputWriter, "%s %s %s %s %s\n",
								eventTypeString(event.Type),
								string(event.Kv.Key),
								string(event.Kv.Value),
								string(event.PrevKv.Key),
								string(event.PrevKv.Value),
							)
						} else {
							_, _ = fmt.Fprintf(outputWriter, "%s %s %s\n",
								eventTypeString(event.Type),
								string(event.Kv.Key),
								string(event.Kv.Value),
							)
						}
					}
				}
			}
		},
	}

	cmd.Flags().BoolVar(&prefix, "prefix", false, "Watch all keys with this prefix")
	cmd.Flags().Int64Var(&startRevision, "start-revision", 0, "Start watching from revision")
	cmd.Flags().BoolVar(&prevKV, "prev-kv", false, "Include previous key-value in events")

	return cmd
}
