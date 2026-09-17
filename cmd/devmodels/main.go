// Command devmodels is a local catalog of Devin models and the evidence for
// choosing between them, usable from a shell or as an MCP server.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/cli"
)

func main() {
	// MCP hosts usually stop a server with SIGTERM; treat it like Ctrl-C so
	// in-flight work is cancelled and the database is closed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// Once shutdown has begun, restore default handling, so a second signal
	// ends a shutdown that hangs.
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := cli.Run(ctx, os.Args[1:], cli.Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv})
	stop()
	os.Exit(code)
}
