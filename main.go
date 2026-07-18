package main

import (
	"fmt"
	"os"

	"github.com/n24q02m/better-drive/internal/cli"
)

func main() {
	attachParentConsole()
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
