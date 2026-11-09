package main

import (
	"context"
	"errors"
	"os"
	"os/signal"

	"openfox/internal/runtime/providercli"
)

func runMCP(args []string) error {
	if len(args) != 2 || args[0] != "provider-cli" {
		return errors.New("usage: openfox mcp provider-cli <feishu|apifox|cnb>")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return providercli.Serve(ctx, args[1], os.Stdin, os.Stdout)
}
