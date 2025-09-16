package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

const helptext = `Usage:
  exifutil rename    # Rename files to their canonical timestamp name.
  exifutil partition # Partition files by their creation date.
`

func main() {
	flagset := flag.NewFlagSet("exifutil", flag.ContinueOnError)
	flagset.Usage = func() {
		fmt.Fprint(os.Stderr, helptext)
	}
	err := flagset.Parse(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, helptext+err.Error())
		os.Exit(1)
	}
	flagArgs := flagset.Args()
	if len(flagArgs) == 0 {
		fmt.Fprintf(os.Stderr, helptext)
		return
	}
	subcmd := flagArgs[0]
	args := flagArgs[1:]
	userInterrupt := make(chan os.Signal, 1)
	signal.Notify(userInterrupt, syscall.SIGTERM, syscall.SIGINT)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-userInterrupt // Soft interrupt.
		cancel()
		<-userInterrupt // Hard interrupt.
		os.Exit(1)
	}()
	exit := func(subcmd string, err error) {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, subcmd+": "+err.Error())
		os.Exit(1)
	}
	switch subcmd {
	case "rename":
		renameCmd, err := RenameCommand(args)
		if err != nil {
			exit(subcmd, err)
		}
		err = renameCmd.Run(ctx)
		if err != nil {
			exit(subcmd, err)
		}
	case "partition":
		partitionCmd, err := PartitionCommand(args)
		if err != nil {
			exit(subcmd, err)
		}
		err = partitionCmd.Run(ctx)
		if err != nil {
			exit(subcmd, err)
		}
	default:
		fmt.Fprintf(os.Stderr, "unrecognized subcommand %q\n", subcmd)
		return
	}
}
