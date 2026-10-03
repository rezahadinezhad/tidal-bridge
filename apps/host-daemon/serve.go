package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"tidalbridge/packages/config"
	"tidalbridge/packages/transport"
)

// ErrAlreadyRunning means a healthy host already serves this data directory.
var ErrAlreadyRunning = errors.New("a Tidal Bridge host is already running")

// Serve runs the host until ctx ends. It migrates legacy AppData state on the
// first start, and treats an existing healthy host as success so duplicate
// launches (login task, launcher, adapters) are harmless.
func Serve(ctx context.Context, dir string, dashboard http.Handler, log io.Writer) error {
	if from, err := config.MigrateLegacy(dir); err != nil {
		fmt.Fprintf(log, "legacy state migration from %s failed: %v\n", from, err)
	} else if from != "" {
		fmt.Fprintf(log, "migrated pairing, profiles and history from %s\n", from)
	}
	h, err := New(dir)
	if err != nil {
		return err
	}
	defer h.Close()
	bind := h.Config().Bind
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var health struct {
			Product string `json:"product"`
		}
		if transport.Request(probe, "http://"+bind, h.Token, "GET", "/v1/health", nil, &health) == nil && health.Product == "Tidal Bridge" {
			return ErrAlreadyRunning
		}
		return fmt.Errorf("listen %s: %w", bind, err)
	}
	h.Start(ctx)
	server := &http.Server{Handler: h.Handler(dashboard), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.Shutdown(shutdown)
		server.Shutdown(shutdown)
	}()
	fmt.Fprintln(log, "Tidal Bridge Host Service listening on http://"+bind, "data", dir)
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
