// Command brk is the short alias for barracks. It is the same program under a
// name that is quicker to type; every command and flag is identical.
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
