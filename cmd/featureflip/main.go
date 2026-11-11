// Command featureflip manages Featureflip feature flags from the terminal.
package main

import (
	"context"
	"os"

	"github.com/canopy-labs/featureflip-cli/internal/cmd"
)

func main() {
	os.Exit(cmd.Execute(context.Background(), os.Args[1:], cmd.DefaultEnv()))
}
