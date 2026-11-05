package signins

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runsCall(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var out map[string]any
	if strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "{") {
		json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func TestTheRunsRoutes(t *testing.T) {
	f := errandFixture(t, "registered")
	got := make(chan answer, 1)
	f.worker.onAgent = asksQuestion(got)
	h := NewRunsHandler(f.svc)

	code, body := runsCall(t, h, "POST", "/api/v1/errands", `{"url":"https://shop.test/a","action":"book a table"}`)
	if code != http.StatusAccepted || body["run"] == nil {
		t.Fatalf("start: %d %v", code, body)
	}
	run := int64(body["run"].(float64))
	soon(t, "the question is held", func() bool { return f.runDetail(t, run).Question != nil })

	r := httptest.NewRequest("GET", "/api/v1/runs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var list []map[string]any
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list) != 1 || list[0]["waiting"] != "question" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	code, body = runsCall(t, h, "GET", fmt.Sprintf("/api/v1/runs/%d", run), "")
	q, _ := body["question"].(map[string]any)
	if code != http.StatusOK || body["url"] != "https://shop.test/a" || q["text"] != "Which day?" {
		t.Fatalf("detail: %d %v", code, body)
	}
	if code, _ := runsCall(t, h, "POST", fmt.Sprintf("/api/v1/runs/%d/answer", run), `{"answer":"Sat"}`); code != http.StatusNoContent {
		t.Fatalf("answer: %d", code)
	}
	f.svc.runs.Wait()
	if code, _ := runsCall(t, h, "POST", fmt.Sprintf("/api/v1/runs/%d/answer", run), `{"answer":"Sun"}`); code != http.StatusConflict {
		t.Fatalf("a late answer: %d", code)
	}
	if code, _ := runsCall(t, h, "POST", fmt.Sprintf("/api/v1/runs/%d/stop", run), `{}`); code != http.StatusConflict {
		t.Fatalf("a late stop: %d", code)
	}
	if code, _ := runsCall(t, h, "GET", "/api/v1/runs/999", ""); code != http.StatusNotFound {
		t.Fatalf("an unknown run: %d", code)
	}
	if code, _ := runsCall(t, h, "POST", "/api/v1/errands", `{"url":"ftp://x","action":"book"}`); code != http.StatusBadRequest {
		t.Fatalf("a bad link: %d", code)
	}
	if code, _ := runsCall(t, h, "GET", "/api/v1/errands", ""); code != http.StatusMethodNotAllowed {
		t.Fatalf("the errands list is the runs': %d", code)
	}
}

func TestARunsBrowserLogIsServed(t *testing.T) {
	f := errandFixture(t, "registered")
	h := NewRunsHandler(f.svc)
	id := f.errand(t, "https://shop.test/a")
	f.svc.runs.Wait()

	// The worker writes the log beside the run's video (its files.py); the engine names no path.
	if body := f.worker.agentRuns[0]; body["run"] != id || body["log_to"] != nil {
		t.Fatalf("body = %v", body)
	}
	logTo := filepath.Join(runDir(f.svc.cfg.RecordingsDir, shopKey, id), "browser-use.log")
	path := fmt.Sprintf("/api/v1/runs/%d/log", id)
	if code, _ := runsCall(t, h, "GET", path, ""); code != http.StatusNotFound {
		t.Fatalf("no log yet: %d", code)
	}
	if _, body := runsCall(t, h, "GET", fmt.Sprintf("/api/v1/runs/%d", id), ""); body["log"] != false {
		t.Fatalf("the page offers a log it hasn't: %v", body["log"])
	}
	os.MkdirAll(filepath.Dir(logTo), 0o700)
	os.WriteFile(logTo, []byte("23:54:35 INFO [browser_use.agent] 📍 Step 1:\n"), 0o600)
	if _, body := runsCall(t, h, "GET", fmt.Sprintf("/api/v1/runs/%d", id), ""); body["log"] != true {
		t.Fatalf("the page doesn't offer its log: %v", body["log"])
	}
	r := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") || !strings.Contains(rec.Body.String(), "Step 1") {
		t.Fatalf("log: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
}

func TestStopOverHTTP(t *testing.T) {
	f := errandFixture(t, "registered")
	f.worker.onAgent = asksQuestion(make(chan answer, 1))
	h := NewRunsHandler(f.svc)
	_, body := runsCall(t, h, "POST", "/api/v1/errands", `{"url":"https://shop.test/a","action":"book a table"}`)
	run := int64(body["run"].(float64))
	soon(t, "the question is held", func() bool { return f.runDetail(t, run).Question != nil })
	if code, _ := runsCall(t, h, "POST", fmt.Sprintf("/api/v1/runs/%d/stop", run), `{}`); code != http.StatusNoContent {
		t.Fatalf("stop: %d", code)
	}
	f.svc.runs.Wait()
	if d := f.runDetail(t, run); d.Reason != "stopped" {
		t.Fatalf("errand = %q", d.Reason)
	}
}
