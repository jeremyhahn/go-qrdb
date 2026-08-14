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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func newTxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tx",
		Short: "Transaction operations",
	}
	cmd.AddCommand(newTxExecuteCmd())
	return cmd
}

func newTxExecuteCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "execute",
		Short: "Execute a transaction from a JSON file",
		RunE: func(cmd *cobra.Command, args []string) error {
			var data []byte
			var err error
			if file == "-" {
				data, err = io.ReadAll(os.Stdin)
				if err != nil {
					return &FileReadError{Path: "stdin", Err: err}
				}
			} else {
				data, err = os.ReadFile(file)
				if err != nil {
					return &FileReadError{Path: file, Err: err}
				}
			}

			var req transport.TransactionRequest
			if err := json.Unmarshal(data, &req); err != nil {
				return err
			}

			ctx := cmd.Context()
			resp, err := client.Transaction().ExecuteTransaction(ctx, &req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				enc := json.NewEncoder(outputWriter)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			case "table":
				_, _ = fmt.Fprintf(outputWriter, "Succeeded: %t\nRevision: %d\n", resp.Succeeded, resp.Revision)
				for i, r := range resp.Responses {
					_, _ = fmt.Fprintf(outputWriter, "Response[%s]: succeeded=%t count=%d\n",
						strconv.Itoa(i), r.Succeeded, r.Count)
				}
				return nil
			default:
				enc := json.NewEncoder(outputWriter)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "JSON file path (use \"-\" for stdin)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}
