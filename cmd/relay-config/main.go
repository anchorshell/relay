// relay-config prepares local configuration without starting a server, opening
// a database, generating secrets, or printing configuration values.
package main

import (
	"fmt"
	"os"

	"github.com/anchorshell/relay/internal/config"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: relay-config PATH_TO_ENV")
		os.Exit(2)
	}
	if err := config.MigrateLegacyEnvFile(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
