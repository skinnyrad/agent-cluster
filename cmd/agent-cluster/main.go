// Command agent-cluster runs a distributed agent cluster on local Ollama models.
//
//	agent-cluster controller   coordinate jobs across workers
//	agent-cluster worker       run tasks on one Ollama model
//	agent-cluster chat         talk to the cluster from the terminal
//	agent-cluster version      print the version
//
// Run "agent-cluster <command> -h" for a command's flags.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `agent-cluster runs a distributed agent cluster on local Ollama models.

Usage:
  agent-cluster <command> [flags]

Commands:
  controller   coordinate jobs across workers
  worker       run tasks on one Ollama model
  chat         talk to the cluster from the terminal
  version      print the version

Run "agent-cluster <command> -h" for a command's flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	var err error
	switch cmd {
	case "controller":
		err = runController(args)
	case "worker":
		err = runWorker(args)
	case "chat":
		err = runChat(args)
	case "version", "--version", "-v":
		fmt.Println("agent-cluster", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// newFlags returns a flag set whose usage line names the subcommand.
func newFlags(name, summary string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "%s\n\nUsage:\n  agent-cluster %s [flags]\n\nFlags:\n", summary, name)
		fs.PrintDefaults()
	}
	return fs
}

// signalContext is cancelled on Ctrl-C or SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
