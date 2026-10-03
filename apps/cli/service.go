package cli

import (
	"errors"

	"tidalbridge/packages/service"
)

var errHostUnavailable = errors.New("Tidal Bridge host did not become ready")

// StartService asks the platform service manager to start the host.
func StartService() error { return service.Start() }
