// Command upstream prints, as JSON, the uses of the method at a position: os.Args are the root, the file, the line
// and the column. Diagnostic lines of Open go to stderr. TestIndexOfAnotherParserIsNotReused builds it with another
// PHP parser than the one go.mod replaces the parser with.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	agentlsp "github.com/hakastein/agent-lsp"
	"github.com/hakastein/agent-lsp/php"
)

func main() {
	line, _ := strconv.Atoi(os.Args[3])
	column, _ := strconv.Atoi(os.Args[4])
	ix, err := php.Open(os.Args[1], func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) })
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer ix.Close()
	refs, err := ix.References(agentlsp.Position{File: os.Args[2], Line: line, Column: column})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(refs.Uses)
}
