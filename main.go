package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.mewis.me/ig-mcp/internal/cli"
)

//go:embed .env.example
var envExample string

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.NewRootCommand(envExample).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "ig-mcp:", err)
		os.Exit(1)
	}
}
