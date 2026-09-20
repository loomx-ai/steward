package main

import (
	"context"
	"fmt"
	"os"

	"github.com/loomx-ai/steward/internal/cli"
)

var version = "dev"

func main() {
	ctx := context.Background()
	// Ask about newer releases and advisories while the command runs; the
	// answer is only ever reported once the command is done.
	cli.StartReleaseCheck(ctx, version)
	err := cli.NewRootCommand(version).ExecuteContext(ctx)
	cli.ReportSecurityAlerts(os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
