package main

import (
	"os"

	"github.com/n24q02m/better-drive/internal/cli"
	"github.com/n24q02m/better-drive/internal/exitcode"
)

func main() {
	attachParentConsole()
	format, err := cli.Execute()
	if err != nil {
		cli.RenderError(os.Stderr, err, format)
		os.Exit(exitcode.Code(err))
	}
}
