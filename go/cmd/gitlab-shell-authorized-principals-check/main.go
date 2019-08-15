package main

import (
	"fmt"
	"os"

	"gitlab.com/gitlab-org/gitlab-shell/go/internal/command"
	"gitlab.com/gitlab-org/gitlab-shell/go/internal/command/readwriter"
	"gitlab.com/gitlab-org/gitlab-shell/go/internal/config"
	"gitlab.com/gitlab-org/gitlab-shell/go/internal/executable"
)

func main() {
	readWriter := &readwriter.ReadWriter{
		Out:    os.Stdout,
		In:     os.Stdin,
		ErrOut: os.Stderr,
	}

	executable, err := executable.New(executable.AuthorizedPrincipalsCheck)
	if err != nil {
		fmt.Fprintln(readWriter.ErrOut, "Failed to determine executable, exiting")
		os.Exit(1)
	}

	config, err := config.NewFromDir(executable.RootDir)
	if err != nil {
		fmt.Fprintln(readWriter.ErrOut, "Failed to read config, exiting")
		os.Exit(1)
	}

	cmd, err := command.New(executable, os.Args[1:], config, readWriter)
	if err != nil {
		// For now this could happen if `SSH_CONNECTION` is not set on
		// the environment
		fmt.Fprintf(readWriter.ErrOut, "%v\n", err)
		os.Exit(1)
	}

	// The command will write to STDOUT on execution or replace the current
	// process in case of the `fallback.Command`
	if err = cmd.Execute(); err != nil {
		fmt.Fprintf(readWriter.ErrOut, "%v\n", err)
		os.Exit(1)
	}
}
