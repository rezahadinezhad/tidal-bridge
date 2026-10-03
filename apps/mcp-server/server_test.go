package mcpserver

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
	"tidalbridge/apps/cli"
)

func TestSDKHandshakeAndTools(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := Server(cli.Client{Endpoint: "http://127.0.0.1:1", Token: "test"})
	a, b := mcp.NewInMemoryTransports()
	serverSession, e := s.Connect(ctx, a, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, e := client.Connect(ctx, b, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	tools, e := session.ListTools(ctx, nil)
	if e != nil || len(tools.Tools) < 7 {
		t.Fatal(tools, e)
	}
	result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: "tidalbridge_status", Arguments: map[string]any{}})
	if e != nil || !result.IsError {
		t.Fatal("unavailable daemon should return a tool error", result, e)
	}
}
