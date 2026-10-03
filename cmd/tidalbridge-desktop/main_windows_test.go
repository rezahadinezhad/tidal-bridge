//go:build windows

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tidalbridge/apps/cli"
	"tidalbridge/packages/config"
)

func TestReadinessAvoidsJobHistoryAndRequiresAuthenticatedIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			t.Errorf("readiness requested expensive endpoint %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		if r.Header.Get("Authorization") != "Bearer paired-token" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"product":"Tidal Bridge","protocol_version":1}`))
	}))
	defer server.Close()
	client := cli.Client{Endpoint: server.URL, Token: "paired-token"}
	if err := ready(client); err != nil {
		t.Fatal(err)
	}
	if !listenerPresent(client.Endpoint) {
		t.Fatal("existing listener was not detected")
	}
	client.Token = "incorrect-token"
	if err := ready(client); err == nil {
		t.Fatal("unauthenticated listener accepted")
	}
	client.Token = "paired-token"
	if err := waitReady(&client, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessReloadsStaleLauncherCredentials(t *testing.T) {
	token := strings.Repeat("b", 48)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"product":"Tidal Bridge","protocol_version":1}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Bind = strings.TrimPrefix(server.URL, "http://")
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := config.Atomic(dir+"/host.token", []byte(token)); err != nil {
		t.Fatal(err)
	}
	client := cli.Client{Endpoint: server.URL, Token: "stale-token", Dir: dir}
	if err := waitReady(&client, 400*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if client.Token != token {
		t.Fatal("dashboard would still receive the stale credential")
	}
}

func TestReadinessNeverAcceptsAnotherListener(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"product":"Other service","protocol_version":1}`))
	}))
	defer server.Close()
	client := cli.Client{Endpoint: server.URL, Token: "paired-token"}
	if err := waitReady(&client, 10*time.Millisecond); err == nil {
		t.Fatal("readiness accepted a different service")
	}
}
