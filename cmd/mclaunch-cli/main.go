// mclaunch-cli: the command line without the window (no CEF), for tests and scripts.
package main

import (
	"fmt"
	"os"

	"mclaunch/internal/cli"
)

func main() {
	ok, err := cli.Run(os.Args[1:])
	if !ok {
		fmt.Print(cli.Usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mclaunch:", err)
		os.Exit(1)
	}
}
