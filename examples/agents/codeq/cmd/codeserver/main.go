// Command codeserver is codeq's tool server. It indexes the Go modules
// under -root and serves five tools over MCP on stdin and stdout.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/panyam/chakra/examples/agents/codeq"
)

func main() {
	root := flag.String("root", "", "a directory holding one Go module per subdirectory")
	flag.Parse()
	ix, err := codeq.Load(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := codeq.NewServer(ix).RunStdio(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
