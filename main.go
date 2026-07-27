// Command barracks manages named bundles of agent skills - loadouts - and
// spawns them into any repository for as long as you need them.
package main

import (
	"os"

	"github.com/tobi404/barracks/internal/cli"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Main(os.Args[1:], version))
}
