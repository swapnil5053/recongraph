// Command recongraph maps a target's web surface as a queryable, diffable graph.
//
// Use it only against systems you are authorised to test.
package main

import (
	"os"

	"github.com/swapnil5053/recongraph/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args))
}
