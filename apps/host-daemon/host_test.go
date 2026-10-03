package host

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
)

func TestRestartPreservesRecentHistoryWithoutReplay(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 45; i++ {
		id := fmt.Sprintf("%048x", i)
		job := protocol.Job{ID: id, State: "COMPLETED", Created: time.Unix(int64(i), 0)}
		if i == 44 {
			job.State = "RUNNING"
		}
		if e := config.SaveJSON(filepath.Join(dir, "jobs", id+".json"), job); e != nil {
			t.Fatal(e)
		}
	}
	h, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	if len(h.jobs) != 40 || len(h.pending) != 0 || h.totalActive != 0 {
		t.Fatal("unexpected history or replay", len(h.jobs), h.pending)
	}
	job, e := h.Job(fmt.Sprintf("%048x", 44))
	if e != nil || job.State != "INTERRUPTED" || job.Finished == nil {
		t.Fatal(job, e)
	}
	if _, present := h.jobs[fmt.Sprintf("%048x", 0)]; present {
		t.Fatal("old history retained in memory")
	}
	// Older results stay readable from disk.
	job, e = h.Job(fmt.Sprintf("%048x", 0))
	if e != nil || job.State != "COMPLETED" {
		t.Fatal(job, e)
	}
}

func TestAuthenticatedHealthDoesNotReturnJobHistory(t *testing.T) {
	h, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.jobs["history"] = &protocol.Job{ID: "history"}
	request := httptest.NewRequest("GET", "/v1/health", nil)
	request.Header.Set("Authorization", "Bearer "+h.Token)
	response := httptest.NewRecorder()
	h.Handler(http.NotFoundHandler()).ServeHTTP(response, request)
	var health map[string]any
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &health) != nil || health["product"] != "Tidal Bridge" {
		t.Fatal(response.Code, response.Body.String())
	}
	if _, present := health["jobs"]; present {
		t.Fatal("health serialized job history")
	}
	request.Header.Del("Authorization")
	response = httptest.NewRecorder()
	h.Handler(http.NotFoundHandler()).ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("health did not require authentication")
	}
}
