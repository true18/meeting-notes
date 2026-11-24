package main

import (
	"fmt"
	"os"

	"meeting-notes/internal/cli"
	"meeting-notes/internal/config"
)

func main() {
	os.Exit(run())
}

func run() int {
	if _, err := config.Load(); err != nil {
		fmt.Fprintln(os.Stderr, "configuration:", err)
		return 1
	}
	command, err := cli.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	fmt.Fprintf(os.Stdout, "command %s is initialized\n", command.Name)
	return 0
}
