package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/dolthub/vitess/go/vt/sqlparser"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
)

func main() {
	tr := engine.NewTranslator()
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		out, err := tr.Translate(line)
		if err != nil {
			fmt.Printf("XLATE-ERR\t%v\t%s\n", err, line)
			continue
		}
		if _, err := sqlparser.Parse(out); err != nil {
			fmt.Printf("PARSE-FAIL\t%v\t%s\t==>\t%s\n", err, line, out)
			continue
		}
		fmt.Printf("OK\t\t%s\t==>\t%s\n", line, out)
	}
}
