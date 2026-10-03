package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"tidalbridge/packages/protocol"
	"time"

	"github.com/coder/websocket"
)

func TestBrowserBoundaries(t *testing.T) {
	base := Spec{URL: "http://localhost:3000/dashboard", Viewport: protocol.Viewport{Name: "phone", Width: 390, Height: 844, DPR: 1}}
	if e := Validate(base); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{"http://example.com:3000/", "http://user:pass@localhost:3000/", "https://localhost:3000/", "http://localhost/"} {
		s := base
		s.URL = bad
		if e := Validate(s); e == nil {
			t.Fatal("unsafe URL accepted", bad)
		}
	}
	base.Viewport.Name = "../file"
	if Validate(base) == nil {
		t.Fatal("unsafe viewport name")
	}
}

func TestBrowserCreatesAndClosesOnlyOwnedTarget(t *testing.T) {
	var base string
	var mu sync.Mutex
	var closed string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/version":
			json.NewEncoder(w).Encode(map[string]any{"webSocketDebuggerUrl": strings.Replace(base, "http:", "ws:", 1) + "/devtools/browser"})
		case "/json/list":
			json.NewEncoder(w).Encode([]targetInfo{{ID: "user-tab", WS: "ws://localhost/devtools/page/user"}, {ID: "owned-tab", WS: "ws://localhost/devtools/page/owned"}})
		case "/devtools/browser":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			for {
				_, data, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				var message struct {
					ID     int            `json:"id"`
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				json.Unmarshal(data, &message)
				result := map[string]any{}
				if message.Method == "Target.createTarget" {
					result["targetId"] = "owned-tab"
				}
				if message.Method == "Target.closeTarget" {
					mu.Lock()
					closed, _ = message.Params["targetId"].(string)
					mu.Unlock()
					result["success"] = true
				}
				response, _ := json.Marshal(map[string]any{"id": message.ID, "result": result})
				if conn.Write(r.Context(), websocket.MessageText, response) != nil {
					return
				}
			}
		default:
			http.Error(w, "HTTP tab creation unsupported", 500)
		}
	}))
	defer server.Close()
	base = server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	target, cleanup, err := createTarget(ctx, base)
	if err != nil || target.ID != "owned-tab" {
		t.Fatal(target, err)
	}
	cleanup()
	mu.Lock()
	defer mu.Unlock()
	if closed != "owned-tab" {
		t.Fatal("closed unowned tab", closed)
	}
}
