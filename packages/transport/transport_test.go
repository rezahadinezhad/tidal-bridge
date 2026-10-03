package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDevices(t *testing.T) {
	v := ParseDevices("List of devices attached\nserial-a device product:p model:Tablet\nserial-b unauthorized\nserial-c offline\n")
	if len(v) != 3 || v[1].State != "unauthorized" {
		t.Fatal(v)
	}
}

func TestForwardSelection(t *testing.T) {
	list := "other tcp:5000 tcp:47832\nphone tcp:5001 tcp:5555\nphone tcp:5002 tcp:47832\n"
	if got := ExistingForward(list, "phone", 47832); got != "5002" {
		t.Fatal(got)
	}
	if got := ExistingForward(list, "absent", 47832); got != "" {
		t.Fatal(got)
	}
}

func TestUploadSendsLengthOfEmptyFiles(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Content-Length")
		w.Write([]byte("{}"))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "__init__.py")
	os.WriteFile(path, nil, 0o644)
	if err := Upload(context.Background(), server.URL, "token", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", path, 0); err != nil {
		t.Fatal(err)
	}
	if got != "0" {
		t.Fatalf("an empty file must be sent with Content-Length 0, got %q", got)
	}
}
