package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"tidalbridge/packages/config"
	"tidalbridge/packages/transport"
)

type Client struct {
	Endpoint, Token, Dir string
	// Quiet suppresses job/route banners (command adapters print one line).
	Quiet bool
}

// New reads the host's configuration and credential without creating either.
func New(dir string) (Client, error) {
	c, e := config.Load(dir)
	if e != nil {
		return Client{}, e
	}
	token, e := config.ReadSecret(dir)
	return Client{Endpoint: "http://" + c.Bind, Token: token, Dir: dir}, e
}
func (c Client) Request(ctx context.Context, method, path string, input, output any) error {
	return transport.Request(ctx, c.Endpoint, c.Token, method, path, input, output)
}

// Healthy reports whether a compatible host answers with this credential.
func (c Client) Healthy(ctx context.Context) bool {
	if c.Token == "" {
		return false
	}
	probe, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	var health struct {
		Product string `json:"product"`
	}
	return c.Request(probe, "GET", "/v1/health", nil, &health) == nil && health.Product == "Tidal Bridge"
}

// Connect returns a client for a running host, asking the installed
// background service to start when nothing answers. The service is started by
// Task Scheduler, outside any MSIX container the caller may live in, so it
// survives the agent app that happened to need it. wait bounds the delay; a
// caller with a local fallback should keep it short.
func Connect(ctx context.Context, dir string, wait time.Duration) (Client, error) {
	c, err := New(dir)
	if err == nil && c.Healthy(ctx) {
		return c, nil
	}
	if startErr := StartService(); startErr != nil {
		if err != nil {
			return c, err
		}
		return c, startErr
	}
	deadline := time.Now().Add(wait)
	for {
		// The first service start may publish the configuration/credential now.
		if fresh, e := New(dir); e == nil {
			c, err = fresh, nil
			if c.Healthy(ctx) {
				return c, nil
			}
		} else {
			err = e
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if err == nil {
				err = errHostUnavailable
			}
			return c, err
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Lazy connects on first use and reconnects after a transport failure, so a
// long-lived caller (the MCP server) survives host restarts.
type Lazy struct {
	Dir  string
	Wait time.Duration
	mu   sync.Mutex
	c    *Client
}

func (l *Lazy) Request(ctx context.Context, method, path string, input, output any) error {
	l.mu.Lock()
	c := l.c
	l.mu.Unlock()
	if c == nil {
		fresh, err := Connect(ctx, l.Dir, l.Wait)
		if err != nil {
			return fmt.Errorf("Tidal Bridge host unavailable; continue locally: %w", err)
		}
		l.mu.Lock()
		l.c = &fresh
		l.mu.Unlock()
		c = &fresh
	}
	err := c.Request(ctx, method, path, input, output)
	var netErr net.Error
	if err != nil && (errors.As(err, &netErr) || strings.Contains(err.Error(), "HTTP 401")) {
		l.mu.Lock()
		l.c = nil
		l.mu.Unlock()
	}
	return err
}
