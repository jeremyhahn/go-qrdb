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

package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"text/tabwriter"
	"unsafe"
)

// outputMode controls how query result rows are formatted when printed.
type outputMode uint8

const (
	// modeTable renders results as a text table using tabwriter alignment.
	modeTable outputMode = iota

	// modeCSV renders results as comma-separated values.
	modeCSV

	// modeJSON renders results as a JSON array of objects.
	modeJSON

	// modeColumn renders results in fixed-width columns (alias for modeTable).
	modeColumn
)

// validOutputModes maps mode name strings to their outputMode constants for
// O(1) lookup during .mode dot-command handling.
var validOutputModes = map[string]outputMode{
	"table":  modeTable,
	"csv":    modeCSV,
	"json":   modeJSON,
	"column": modeColumn,
}

// shell provides an interactive SQL REPL backed by a *sql.DB. Query results
// are formatted according to the current outputMode. Multi-line SQL is
// accumulated until a statement-terminating semicolon is found.
type shell struct {
	db       *sql.DB
	out      io.Writer
	mode     outputMode
	headers  bool   // whether column headers are printed
	nullText string // text substituted for NULL values in output
}

// newShell constructs a shell with sensible defaults: table output mode,
// headers enabled, and an empty NULL display string. out defaults to
// os.Stdout when nil.
func newShell(db *sql.DB, out io.Writer) *shell {
	if out == nil {
		out = os.Stdout
	}
	return &shell{
		db:      db,
		out:     out,
		mode:    modeTable,
		headers: true,
	}
}

// run starts the REPL loop. It reads from input until EOF or a .quit / .exit
// dot-command is encountered. Prompts are displayed only when input is an
// interactive terminal (os.Stdin).
//
// SQL errors and dot-command errors are printed and the loop continues;
// only genuine read errors cause run to return a non-nil error.
func (s *shell) run(input io.Reader) error {
	interactive := isTerminal(input)
	scanner := bufio.NewScanner(input)

	var buf strings.Builder

	for {
		if interactive {
			if buf.Len() == 0 {
				fmt.Fprint(s.out, "qrdb> ")
			} else {
				fmt.Fprint(s.out, "   ...> ")
			}
		}

		if !scanner.Scan() {
			// EOF or read error.
			if err := scanner.Err(); err != nil {
				return err
			}
			// Clean EOF: flush any pending incomplete statement.
			if buf.Len() > 0 {
				query := strings.TrimSpace(buf.String())
				if query != "" {
					if err := s.executeSQL(query); err != nil {
						fmt.Fprintln(s.out, "Error:", err)
					}
				}
			}
			if interactive {
				fmt.Fprintln(s.out)
			}
			return nil
		}

		line := scanner.Text()

		// A dot-command is handled immediately; it never contributes to a
		// multi-line SQL buffer.
		if buf.Len() == 0 && strings.HasPrefix(strings.TrimSpace(line), ".") {
			quit, err := s.executeDotCommand(strings.TrimSpace(line))
			if err != nil {
				fmt.Fprintln(s.out, "Error:", err)
			}
			if quit {
				return nil
			}
			continue
		}

		// Accumulate the line into the SQL buffer.
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(line)

		// Execute once a statement-terminating semicolon is present.
		if statementComplete(buf.String()) {
			query := strings.TrimSpace(buf.String())
			buf.Reset()
			if query == "" || query == ";" {
				continue
			}
			if err := s.executeSQL(query); err != nil {
				fmt.Fprintln(s.out, "Error:", err)
			}
		}
	}
}

// executeDotCommand processes a single dot-command line. It returns
// (true, nil) when the command requests shell exit, or (false, err) on an
// unrecognised or malformed command.
func (s *shell) executeDotCommand(line string) (quit bool, err error) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return false, nil
	}

	cmd := strings.ToLower(parts[0])

	switch cmd {
	case ".quit", ".exit":
		return true, nil

	case ".tables":
		return false, s.dotTables()

	case ".schema":
		table := ""
		if len(parts) > 1 {
			table = parts[1]
		}
		return false, s.dotSchema(table)

	case ".mode":
		if len(parts) < 2 {
			return false, &ShellError{Err: &ConfigError{
				Field:  ".mode",
				Reason: "usage: .mode table|csv|json|column",
			}}
		}
		m, ok := validOutputModes[strings.ToLower(parts[1])]
		if !ok {
			return false, &ShellError{Err: &ConfigError{
				Field:  ".mode",
				Reason: "unknown mode " + parts[1] + ": valid modes are table, csv, json, column",
			}}
		}
		s.mode = m

	case ".headers":
		if len(parts) < 2 {
			return false, &ShellError{Err: &ConfigError{
				Field:  ".headers",
				Reason: "usage: .headers on|off",
			}}
		}
		switch strings.ToLower(parts[1]) {
		case "on":
			s.headers = true
		case "off":
			s.headers = false
		default:
			return false, &ShellError{Err: &ConfigError{
				Field:  ".headers",
				Reason: "usage: .headers on|off",
			}}
		}

	case ".nullvalue":
		if len(parts) < 2 {
			s.nullText = ""
		} else {
			s.nullText = parts[1]
		}

	case ".databases":
		return false, s.dotDatabases()

	case ".pragma":
		return false, s.dotPragma(parts[1:])

	case ".help":
		s.printHelp()

	default:
		return false, &ShellError{Err: &ConfigError{
			Field:  cmd,
			Reason: "unknown dot-command; type .help for available commands",
		}}
	}

	return false, nil
}

// executeSQL runs query against the database and prints results. SQL errors
// are returned so the caller can display them without terminating the session.
func (s *shell) executeSQL(query string) error {
	rows, err := s.db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return err
	}

	// SELECT-style queries return columns; DDL / DML may return none.
	if len(cols) == 0 {
		return rows.Err()
	}

	return s.printRows(cols, rows)
}

// cell holds a single result column value. isNull is set when the SQL value
// was NULL; s holds the string representation for non-NULL values.
type cell struct {
	s      string
	isNull bool
}

// display returns the formatted string for the cell. NULL cells return
// nullText; non-NULL cells return their string value.
func (c cell) display(nullText string) string {
	if c.isNull {
		return nullText
	}
	return c.s
}

// printRows collects all rows from the result set and delegates to the
// appropriate formatter based on s.mode.
func (s *shell) printRows(cols []string, rows *sql.Rows) error {
	scanBuf := make([]interface{}, len(cols))
	rawBuf := make([][]byte, len(cols))
	for i := range rawBuf {
		scanBuf[i] = &rawBuf[i]
	}

	var allRows [][]cell
	for rows.Next() {
		if err := rows.Scan(scanBuf...); err != nil {
			return err
		}
		row := make([]cell, len(cols))
		for i, b := range rawBuf {
			if b == nil {
				row[i] = cell{isNull: true}
			} else {
				row[i] = cell{s: string(b)}
			}
		}
		allRows = append(allRows, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	switch s.mode {
	case modeCSV:
		return s.printCSV(cols, allRows)
	case modeJSON:
		return s.printJSON(cols, allRows)
	default: // modeTable, modeColumn
		return s.printTable(cols, allRows)
	}
}

// printTable formats cols and allRows as an aligned text table using
// text/tabwriter. Column headers are printed when s.headers is true.
func (s *shell) printTable(cols []string, allRows [][]cell) error {
	tw := tabwriter.NewWriter(s.out, 0, 0, 2, ' ', 0)

	if s.headers {
		fmt.Fprintln(tw, strings.Join(cols, "\t"))
		// Separator row of dashes sized to each column name.
		dashes := make([]string, len(cols))
		for i, c := range cols {
			dashes[i] = strings.Repeat("-", len(c))
		}
		fmt.Fprintln(tw, strings.Join(dashes, "\t"))
	}

	fields := make([]string, len(cols))
	for _, row := range allRows {
		for i, c := range row {
			fields[i] = c.display(s.nullText)
		}
		fmt.Fprintln(tw, strings.Join(fields, "\t"))
	}

	return tw.Flush()
}

// printCSV formats cols and allRows as RFC 4180-style comma-separated values.
// Fields containing commas, double-quotes, or newlines are quoted.
// Column headers are printed when s.headers is true.
func (s *shell) printCSV(cols []string, allRows [][]cell) error {
	if s.headers {
		fmt.Fprintln(s.out, csvRow(cols))
	}
	fields := make([]string, len(cols))
	for _, row := range allRows {
		for i, c := range row {
			fields[i] = c.display(s.nullText)
		}
		fmt.Fprintln(s.out, csvRow(fields))
	}
	return nil
}

// printJSON formats cols and allRows as a JSON array of objects. Each object
// maps column names to their string values. SQL NULL values are rendered as
// the JSON null literal; non-NULL values are rendered as JSON strings.
func (s *shell) printJSON(cols []string, allRows [][]cell) error {
	fmt.Fprintln(s.out, "[")
	for i, row := range allRows {
		fmt.Fprint(s.out, "  {")
		for j, col := range cols {
			c := row[j]
			var jsonVal string
			if c.isNull {
				jsonVal = "null"
			} else {
				jsonVal = jsonQuote(c.s)
			}
			if j > 0 {
				fmt.Fprint(s.out, ", ")
			}
			fmt.Fprintf(s.out, "%s: %s", jsonQuote(col), jsonVal)
		}
		fmt.Fprint(s.out, "}")
		if i < len(allRows)-1 {
			fmt.Fprintln(s.out, ",")
		} else {
			fmt.Fprintln(s.out)
		}
	}
	fmt.Fprintln(s.out, "]")
	return nil
}

// ---------------------------------------------------------------------------
// Dot-command helpers
// ---------------------------------------------------------------------------

// dotTables queries sqlite_master for all user tables and prints their names.
func (s *shell) dotTables() error {
	rows, err := s.db.Query(
		"SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		fmt.Fprintln(s.out, name)
	}
	return rows.Err()
}

// dotSchema prints the CREATE TABLE statement for the given table name. When
// table is empty, all table schemas are printed.
func (s *shell) dotSchema(table string) error {
	var (
		query string
		args  []interface{}
	)
	if table == "" {
		query = "SELECT sql FROM sqlite_master WHERE type='table' AND sql IS NOT NULL ORDER BY name"
	} else {
		query = "SELECT sql FROM sqlite_master WHERE type='table' AND name=? AND sql IS NOT NULL"
		args = []interface{}{table}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var ddl string
		if err := rows.Scan(&ddl); err != nil {
			return err
		}
		fmt.Fprintln(s.out, ddl+";")
	}
	return rows.Err()
}

// dotDatabases lists attached databases via the SQLite PRAGMA database_list.
func (s *shell) dotDatabases() error {
	rows, err := s.db.Query("PRAGMA database_list")
	if err != nil {
		return err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return err
	}

	return s.printRows(cols, rows)
}

// dotPragma executes a PRAGMA statement from the .pragma dot-command.
// args contains the tokens following ".pragma", e.g. ["qrdb_consistency"] or
// ["qrdb_consistency", "linearizable"] or ["journal_mode", "=", "wal"].
func (s *shell) dotPragma(args []string) error {
	if len(args) == 0 {
		return &ShellError{Err: &ConfigError{
			Field:  ".pragma",
			Reason: "usage: .pragma NAME [VALUE]",
		}}
	}

	name := args[0]
	var query string

	if len(args) == 1 {
		query = "PRAGMA " + name
	} else {
		// Accept both "NAME VALUE" and "NAME = VALUE" forms.
		value := strings.Join(args[1:], " ")
		value = strings.TrimPrefix(value, "=")
		value = strings.TrimSpace(value)
		query = "PRAGMA " + name + " = " + value
	}

	return s.executeSQL(query)
}

// printHelp writes the available dot-commands to s.out.
func (s *shell) printHelp() {
	help := []struct{ cmd, desc string }{
		{".quit / .exit", "Exit the shell"},
		{".tables", "List all tables"},
		{".schema [TABLE]", "Show CREATE statement for TABLE (or all tables)"},
		{".mode MODE", "Set output mode: table, csv, json, column"},
		{".headers on|off", "Toggle column header display"},
		{".nullvalue TEXT", "Set text displayed for NULL values"},
		{".databases", "Show attached databases"},
		{".pragma NAME [VALUE]", "Execute PRAGMA; qrdb_consistency, qrdb_write_mode, qrdb_status"},
		{".help", "Show this help message"},
	}

	tw := tabwriter.NewWriter(s.out, 0, 0, 2, ' ', 0)
	for _, h := range help {
		fmt.Fprintf(tw, "  %s\t%s\n", h.cmd, h.desc)
	}
	_ = tw.Flush()
}

// ---------------------------------------------------------------------------
// Formatting helpers
// ---------------------------------------------------------------------------

// csvRow encodes a slice of field strings as a single CSV row. Fields that
// contain a comma, double-quote, or newline are wrapped in double-quotes with
// internal double-quotes doubled per RFC 4180.
func csvRow(fields []string) string {
	var sb strings.Builder
	for i, f := range fields {
		if i > 0 {
			sb.WriteByte(',')
		}
		if strings.ContainsAny(f, `,"\n\r`) {
			sb.WriteByte('"')
			sb.WriteString(strings.ReplaceAll(f, `"`, `""`))
			sb.WriteByte('"')
		} else {
			sb.WriteString(f)
		}
	}
	return sb.String()
}

// jsonQuote returns s wrapped in JSON double-quotes with the required escape
// sequences applied. Only the characters mandated by JSON (RFC 8259) are
// escaped: double-quote, backslash, and the C0 control characters.
func jsonQuote(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&sb, `\u%04x`, r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// statementComplete reports whether s contains a complete SQL statement, i.e.
// whether there is a semicolon that is not inside a string literal or comment.
// This is a simplified heuristic sufficient for interactive shell use.
func statementComplete(s string) bool {
	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}

		if inBlockComment {
			if ch == '*' && i+1 < len(runes) && runes[i+1] == '/' {
				inBlockComment = false
				i++
			}
			continue
		}

		if inSingle {
			if ch == '\'' {
				// Two consecutive single-quotes are an escaped quote, not end.
				if i+1 < len(runes) && runes[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
			continue
		}

		if inDouble {
			if ch == '"' {
				inDouble = false
			}
			continue
		}

		switch ch {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '-':
			if i+1 < len(runes) && runes[i+1] == '-' {
				inLineComment = true
				i++
			}
		case '/':
			if i+1 < len(runes) && runes[i+1] == '*' {
				inBlockComment = true
				i++
			}
		case ';':
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Terminal detection
// ---------------------------------------------------------------------------

// isTerminal reports whether r is an interactive terminal. It returns true
// only when r is exactly os.Stdin and its underlying file descriptor is
// associated with a terminal device (as determined by TIOCGWINSZ ioctl).
// On non-Unix platforms or when the ioctl is unavailable the function always
// returns false, causing the shell to run in non-interactive (no-prompt) mode.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok || f != os.Stdin {
		return false
	}
	return isFdTerminal(f.Fd())
}

// isFdTerminal calls the TIOCGWINSZ ioctl to determine whether fd is a tty.
// Any failure (ENOTTY, EINVAL, etc.) is treated as "not a terminal".
func isFdTerminal(fd uintptr) bool {
	// winsize is the minimal struct for TIOCGWINSZ; we only care whether the
	// call succeeds, not the actual window dimensions.
	var ws [4]uint16
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		syscall.TIOCGWINSZ,
		uintptr(unsafe.Pointer(&ws[0])),
	)
	return errno == 0
}
