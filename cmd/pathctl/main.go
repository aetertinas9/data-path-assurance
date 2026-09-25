// Command pathctl explains a GPU device or a node. This build supports only
// the offline path: it reads a snapshot artifact written by
// path-agent --fixture-root and an offline fleet file, and writes the
// explanation to stdout as text or JSON. The live transport path is refused.
//
// Exit codes: 0 explanation written (or help), 1 internal error, 2 usage
// error, 4 not found, 5 live path unsupported, 6 invalid artifact, 7 invalid
// fleet file, 8 size bound exceeded.
package main

import (
	"context"
	"os"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/cli/explain"
	"github.com/aetertinas9/data-path-assurance/internal/offline"
)

func main() {
	command := explain.NewCommand(app.ExplainFleet, offline.NewSource, os.Stdout, os.Stderr)
	os.Exit(command.Run(context.Background(), os.Args[1:]))
}
