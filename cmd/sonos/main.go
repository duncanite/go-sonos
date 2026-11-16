// sonos controls the Sonos speakers of a household on the LAN; the command
// is internal/cli.
package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/mycophonic/primordium/app"
	"github.com/mycophonic/primordium/app/shutdown"
	"github.com/mycophonic/primordium/network"

	"github.com/duncanite/go-sonos/internal/cli"
)

// requestTimeout bounds one request to a speaker.
const requestTimeout = 10 * time.Second

func main() {
	app.Main(&app.Options{Name: "sonos"}, func(ctx context.Context) error {
		client := &http.Client{Transport: network.NewTransport(), Timeout: requestTimeout}

		if code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, client); code != cli.ExitOK {
			return shutdown.ExitError(code)
		}

		return nil
	})
}
