package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
	"tidalbridge/packages/transport"
)

type Viewport = protocol.Viewport
type Spec = protocol.RenderSpec

func Validate(s Spec) error {
	u, e := url.Parse(s.URL)
	if e != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.User != nil {
		return fmt.Errorf("browser jobs require a loopback HTTP development-server URL")
	}
	port, e := strconv.Atoi(u.Port())
	if e != nil || port < 1024 || port > 65535 {
		return fmt.Errorf("URL needs an explicit development port >=1024")
	}
	v := s.Viewport
	if v.Width < 240 || v.Width > 2560 || v.Height < 240 || v.Height > 2000 || v.DPR < 1 || v.DPR > 3 || float64(v.Width*v.Height)*v.DPR*v.DPR > 8e6 {
		return fmt.Errorf("viewport exceeds bounded capture limits")
	}
	if v.Name == "" || strings.ContainsAny(v.Name, "/\\:. ") {
		return fmt.Errorf("invalid viewport name")
	}
	return nil
}
func Socket(ctx context.Context, adb, serial string) string {
	value, _ := transport.ADB(ctx, adb, serial, "shell", "cat", "/proc/net/unix")
	for _, line := range strings.Split(value, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			socket := strings.TrimPrefix(fields[len(fields)-1], "@")
			if socket == "chrome_devtools_remote" {
				return socket
			}
		}
	}
	return ""
}

type CDP struct {
	conn             *websocket.Conn
	id               int
	Console, Network []json.RawMessage
}

type targetInfo struct {
	ID string `json:"id"`
	WS string `json:"webSocketDebuggerUrl"`
}

func readJSON(ctx context.Context, address string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return err
	}
	resp, err := transport.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Chrome debugging endpoint: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func debugURL(raw, base string) (string, error) {
	u, err := url.Parse(raw)
	b, _ := url.Parse(base)
	if err != nil || u.Scheme != "ws" || u.User != nil || u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" {
		return "", fmt.Errorf("unexpected CDP websocket endpoint")
	}
	u.Host = b.Host
	return u.String(), nil
}

// Android Chrome can reject the desktop HTTP /json/new endpoint. Create an
// owned tab through the browser protocol instead; never reuse a user's tab.
func createTarget(ctx context.Context, base string) (targetInfo, func(), error) {
	var version struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if err := readJSON(ctx, base+"/json/version", &version); err != nil {
		return targetInfo{}, nil, err
	}
	address, err := debugURL(version.WS, base)
	if err != nil {
		return targetInfo{}, nil, err
	}
	conn, _, err := websocket.Dial(ctx, address, nil)
	if err != nil {
		return targetInfo{}, nil, err
	}
	conn.SetReadLimit(1 << 20)
	browser := CDP{conn: conn}
	var created struct {
		ID string `json:"targetId"`
	}
	if err = browser.Call(ctx, "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
		conn.CloseNow()
		return targetInfo{}, nil, fmt.Errorf("Chrome cannot create an isolated debugging target: %w", err)
	}
	cleanup := func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		browser.Call(closeCtx, "Target.closeTarget", map[string]any{"targetId": created.ID}, nil)
		conn.CloseNow()
	}
	for range 20 {
		var targets []targetInfo
		if err = readJSON(ctx, base+"/json/list", &targets); err != nil {
			cleanup()
			return targetInfo{}, nil, err
		}
		for _, target := range targets {
			if target.ID == created.ID && target.WS != "" {
				return target, cleanup, nil
			}
		}
		select {
		case <-ctx.Done():
			cleanup()
			return targetInfo{}, nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	cleanup()
	return targetInfo{}, nil, fmt.Errorf("Chrome did not expose the owned tab's debugging endpoint")
}

func (c *CDP) Call(ctx context.Context, method string, params any, out any) error {
	c.id++
	id := c.id
	b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if e := c.conn.Write(ctx, websocket.MessageText, b); e != nil {
		return e
	}
	for {
		_, data, e := c.conn.Read(ctx)
		if e != nil {
			return e
		}
		var message struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Params json.RawMessage `json:"params"`
			Error  json.RawMessage `json:"error"`
		}
		if e = json.Unmarshal(data, &message); e != nil {
			return e
		}
		if message.ID == id {
			if len(message.Error) > 0 {
				return fmt.Errorf("CDP %s: %s", method, message.Error)
			}
			if out != nil {
				return json.Unmarshal(message.Result, out)
			}
			return nil
		}
		if strings.HasPrefix(message.Method, "Runtime.console") && len(c.Console) < 200 {
			c.Console = append(c.Console, message.Params)
		}
		if strings.HasPrefix(message.Method, "Network.") && len(c.Network) < 500 {
			c.Network = append(c.Network, message.Params)
		}
	}
}
func Capture(ctx context.Context, adb string, node protocol.WorkerNode, s Spec, dest string) error {
	if e := Validate(s); e != nil {
		return e
	}
	socket := Socket(ctx, adb, node.Serial)
	if socket == "" {
		return fmt.Errorf("Android Chrome debugging socket is unavailable; open Chrome on the unlocked device")
	}
	u, _ := url.Parse(s.URL)
	devPort := u.Port()
	mappings, _ := transport.ADB(ctx, adb, node.Serial, "reverse", "--list")
	mapping := "tcp:" + devPort
	owned := !strings.Contains(mappings, mapping+" "+mapping)
	if owned {
		if _, e := transport.ADB(ctx, adb, node.Serial, "reverse", "--no-rebind", mapping, mapping); e != nil {
			return e
		}
		defer transport.ADB(context.Background(), adb, node.Serial, "reverse", "--remove", mapping)
	}
	forwarded, e := transport.ADB(ctx, adb, node.Serial, "forward", "tcp:0", "localabstract:"+socket)
	if e != nil {
		return e
	}
	defer transport.ADB(context.Background(), adb, node.Serial, "forward", "--remove", "tcp:"+forwarded)
	base := "http://127.0.0.1:" + forwarded
	target, closeTarget, e := createTarget(ctx, base)
	if e != nil {
		return e
	}
	defer closeTarget()
	wsURL, e := debugURL(target.WS, base)
	if e != nil {
		return e
	}
	conn, _, e := websocket.Dial(ctx, wsURL, nil)
	if e != nil {
		return e
	}
	defer conn.CloseNow()
	conn.SetReadLimit(16 << 20)
	c := CDP{conn: conn}
	for _, method := range []string{"Page.enable", "Runtime.enable", "Network.enable"} {
		if e = c.Call(ctx, method, map[string]any{}, nil); e != nil {
			return e
		}
	}
	v := s.Viewport
	if e = c.Call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{"width": v.Width, "height": v.Height, "deviceScaleFactor": v.DPR, "mobile": false}, nil); e != nil {
		return e
	}
	if e = c.Call(ctx, "Page.navigate", map[string]any{"url": s.URL}, nil); e != nil {
		return e
	}
	deadline := time.Now().Add(15 * time.Second)
	loaded := false
	for time.Now().Before(deadline) {
		var result struct {
			Result struct {
				Value any `json:"value"`
			} `json:"result"`
		}
		if e = c.Call(ctx, "Runtime.evaluate", map[string]any{"expression": "document.readyState", "returnByValue": true}, &result); e != nil {
			return e
		}
		if result.Result.Value == "complete" {
			loaded = true
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !loaded {
		return fmt.Errorf("development page did not load within 15 seconds")
	}
	// Fonts and one animation frame settle without an arbitrary multi-second sleep.
	if e = c.Call(ctx, "Runtime.evaluate", map[string]any{"expression": "document.fonts.ready.then(()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))))", "awaitPromise": true}, nil); e != nil {
		return e
	}
	var screenshot struct {
		Data string `json:"data"`
	}
	if e = c.Call(ctx, "Page.captureScreenshot", map[string]any{"format": "png", "fromSurface": true, "captureBeyondViewport": false}, &screenshot); e != nil {
		return e
	}
	data, e := base64.StdEncoding.DecodeString(screenshot.Data)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(dest, 0700); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(dest, v.Name+".png"), data, 0600); e != nil {
		return e
	}
	var layout any
	if e = c.Call(ctx, "Runtime.evaluate", map[string]any{"expression": "JSON.stringify({width:innerWidth,height:innerHeight,dpr:devicePixelRatio,scrollWidth:document.documentElement.scrollWidth,scrollHeight:document.documentElement.scrollHeight,url:location.href,userAgent:navigator.userAgent})", "returnByValue": true}, &layout); e != nil {
		return e
	}
	var browserVersion any
	_ = c.Call(ctx, "Browser.getVersion", map[string]any{}, &browserVersion)
	config.SaveJSON(filepath.Join(dest, "layout-metadata.json"), layout)
	config.SaveJSON(filepath.Join(dest, "console.json"), c.Console)
	config.SaveJSON(filepath.Join(dest, "network-summary.json"), c.Network)
	return config.SaveJSON(filepath.Join(dest, "manifest.json"), map[string]any{"device_id": node.ID, "device_model": node.Profile.Capabilities.Model, "android_version": node.Profile.Capabilities.AndroidVersion, "browser": browserVersion, "viewport": v, "orientation": map[bool]string{true: "landscape", false: "portrait"}[v.Width > v.Height], "captured_at": time.Now().UTC(), "rendering_engine": "Android Chrome", "tier": "A: real Android rendering with viewport emulation; desktop fidelity is unverified", "physical_device": true, "viewport_emulated": true, "file": v.Name + ".png", "url": s.URL})
}
