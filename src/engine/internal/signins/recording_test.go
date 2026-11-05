package signins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"concierge/engine/internal/browser"
)

// recorder stands in for the worker's recording: it notes each request, and (like the worker) for a
// request naming its run and part says `recording` and leaves a file of `size` bytes where the shared
// layout puts that part.
type recorder struct {
	inner Runner
	// dir is the recordings root, as the worker has it.
	dir string
	mu  sync.Mutex
	// say is whether the worker reports `recording`; size is the part's file it leaves (none when
	// negative).
	say  bool
	size int64
	// asked is each request's body, in order.
	asked []browser.Message
}

func (r *recorder) Request(ctx context.Context, typ string, body browser.Message) (browser.Message, error) {
	return r.RequestAsk(ctx, typ, body, nil, nil)
}

func (r *recorder) RequestAsk(ctx context.Context, typ string, body browser.Message, ask browser.Ask, progress browser.Progress) (browser.Message, error) {
	if typ == "run_agent" {
		r.mu.Lock()
		r.asked = append(r.asked, body)
		say, size := r.say, r.size
		r.mu.Unlock()
		if part, ok := body["part"].(int); ok {
			if say && progress != nil {
				progress("recording", browser.Message{})
			}
			if size >= 0 {
				if err := writePart(partPath(r.dir, body["site"].(string), body["run"].(int64), part), size); err != nil {
					return nil, err
				}
			}
		}
	}
	return r.inner.RequestAsk(ctx, typ, body, ask, progress)
}

// partPath is where the worker writes a run's part n (its files.py).
func partPath(dir, site string, run int64, n int) string {
	return filepath.Join(runDir(dir, site, run), fmt.Sprintf("%d.mp4", n))
}

// writePart leaves a part's file of size bytes, as the worker's ffmpeg does, its directory made.
func writePart(path string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(size)
}

// next is where the worker would write the run's next part: the path of what log.record() names,
// its directory made.
func (f *recordingFixture) next(t *testing.T, log *runLog) string {
	t.Helper()
	msg := log.record()
	part, ok := msg["part"].(int)
	if !ok || msg["run"] != log.id {
		t.Fatalf("record() = %v", msg)
	}
	path := partPath(f.dir, shopKey, log.id, part)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	return path
}

type recordingFixture struct {
	*fixture
	h   http.Handler
	rec *recorder
	dir string
}

// newRecordingFixture is a fixture whose box has screens and records into a directory; its clock
// moves a second at every reading.
func newRecordingFixture(t *testing.T) *recordingFixture {
	t.Helper()
	f := newFixture(t)
	rec := &recorder{inner: f.svc.cfg.Worker, dir: f.svc.cfg.RecordingsDir, say: true, size: 4}
	f.svc.cfg.Worker = rec
	var tick atomic.Int64
	f.svc.cfg.Now = func() time.Time { return testNow.Add(time.Duration(tick.Add(1)) * time.Second) }
	return &recordingFixture{fixture: f, h: NewHandler(f.svc), rec: rec, dir: f.svc.cfg.RecordingsDir}
}

type runView struct {
	Parts  []RunPart  `json:"parts"`
	Clicks []RunClick `json:"clicks"`
}

func (f *recordingFixture) runURL(id int64) string {
	return fmt.Sprintf("/api/v1/signins/sites/%s/runs/%d", shopKey, id)
}

// view is the run's parts (as its errand page has them) and clicks, and the two as JSON.
func (f *recordingFixture) view(t *testing.T, id int64) (runView, string) {
	t.Helper()
	ctx := context.Background()
	r, _, found, err := f.store.Run(ctx, shopKey, id)
	if err != nil || !found {
		t.Fatalf("run %d: %v %v", id, found, err)
	}
	var v runView
	if v.Parts, err = f.svc.Parts(ctx, r); err != nil {
		t.Fatal(err)
	}
	if v.Clicks, err = f.store.Clicks(ctx, id); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	return v, string(raw)
}

func (f *recordingFixture) get(path string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

// errand runs an errand to its end and returns its run's id.
func (f *recordingFixture) errand(t *testing.T) int64 {
	t.Helper()
	id, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://shop.test/a", Action: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	return id
}

// open starts a run by hand and returns its log and directory.
func (f *recordingFixture) open(t *testing.T) (*runLog, string) {
	t.Helper()
	log, err := f.svc.startRun(context.Background(), shopKey, "errand")
	if err != nil {
		t.Fatal(err)
	}
	return log, filepath.Join(f.dir, shopKey, fmt.Sprint(log.id))
}

func TestARunsPartsAreItsRequests(t *testing.T) {
	f := newRecordingFixture(t)
	id := f.errand(t)
	v, raw := f.view(t, id)
	if len(f.rec.asked) != 1 {
		t.Fatalf("the worker was asked %d times", len(f.rec.asked))
	}
	if body := f.rec.asked[0]; body["run"] != id || body["part"] != 1 {
		t.Errorf("records run %v part %v, want run %d part 1", body["run"], body["part"], id)
	}
	if len(v.Parts) != 1 || v.Parts[0].N != 1 {
		t.Fatalf("parts = %+v in %s", v.Parts, raw)
	}
	for _, p := range v.Parts {
		if p.DurationMS <= 0 || p.StartedAt.IsZero() {
			t.Errorf("part %+v", p)
		}
	}
	if !strings.Contains(raw, `"clicks":[]`) {
		t.Errorf("clicks are not an empty list: %s", raw)
	}
}

// The worker computes every path: a recorded request names its run and part, never a file, a budget
// or a log.
func TestRecordedRequestCarriesRunAndPart(t *testing.T) {
	f := newRecordingFixture(t)
	f.signedUp(t, "registered")
	if err := f.store.SetFootprint(context.Background(), shopKey, "https://www.shop.test/me", nil); err != nil {
		t.Fatal(err)
	}
	id, err := f.svc.CheckSignIn(context.Background(), shopKey)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	reads := f.worker.read()
	if len(reads) != 1 {
		t.Fatalf("reads = %v", reads)
	}
	body := reads[0]
	if body["run"] != id || body["part"] != 1 {
		t.Errorf("run %v part %v, want run %d part 1", body["run"], body["part"], id)
	}
	for _, gone := range []string{"record_to", "record_max_bytes", "log_to"} {
		if _, ok := body[gone]; ok {
			t.Errorf("the check sends %s: %v", gone, body)
		}
	}
	// A second part of the same run is the next number.
	log := &runLog{store: f.store, now: f.svc.cfg.Now, id: id, site: shopKey, recorded: true}
	log.partStarted()
	if msg := log.record(); msg["part"] != 2 {
		t.Errorf("next part = %v", msg)
	}
}

func TestNoRecordingsSendsNoRunOrPart(t *testing.T) {
	for name, clear := range map[string]func(*Config){
		"no recordings dir": func(c *Config) { c.RecordingsDir = "" },
		"no screens":        func(c *Config) { c.ScreensDir = "" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newRecordingFixture(t)
			clear(&f.svc.cfg)
			id := f.errand(t)
			if len(f.rec.asked) != 1 {
				t.Fatalf("asked %d", len(f.rec.asked))
			}
			for _, body := range f.rec.asked {
				if _, ok := body["run"]; ok {
					t.Errorf("body = %v", body)
				}
				if _, ok := body["part"]; ok {
					t.Errorf("body = %v", body)
				}
			}
			if v, _ := f.view(t, id); len(v.Parts) != 0 {
				t.Errorf("parts = %+v", v.Parts)
			}
			if entries, _ := os.ReadDir(f.dir); len(entries) != 0 {
				t.Errorf("made %v", entries)
			}
		})
	}
}

func TestAPartThatNeverStartedIsNotListed(t *testing.T) {
	f := newRecordingFixture(t)
	f.rec.say = false
	id := f.errand(t)
	if v, raw := f.view(t, id); len(v.Parts) != 0 || !strings.Contains(raw, `"parts":[]`) {
		t.Fatalf("parts = %s", raw)
	}
}

func TestAnEmptyFileIsNotListed(t *testing.T) {
	f := newRecordingFixture(t)
	f.rec.size = 0
	id := f.errand(t)
	if v, _ := f.view(t, id); len(v.Parts) != 0 {
		t.Fatalf("parts = %+v", v.Parts)
	}
	if rec := f.get(f.runURL(id) + "/recording/1"); rec.Code != http.StatusNotFound {
		t.Fatalf("an empty file was served: %d", rec.Code)
	}
}

func TestARunningRunsUnfinishedPartIsNotServed(t *testing.T) {
	f := newRecordingFixture(t)
	log, dir := f.open(t)
	file := f.next(t, log)
	if err := os.WriteFile(file, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, time.Time{}, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	log.progress("recording", browser.Message{})
	url := f.runURL(log.id) + "/recording/1"
	if rec := f.get(url); rec.Code != http.StatusNotFound {
		t.Fatalf("a part still being written: %d", rec.Code)
	}
	if v, _ := f.view(t, log.id); len(v.Parts) != 0 {
		t.Fatalf("parts = %+v", v.Parts)
	}
	// The run ending ends its part, which then plays up to where the run stopped.
	log.end("ended", "the worker stopped")
	if rec := f.get(url); rec.Code != http.StatusOK || rec.Body.String() != "video" {
		t.Fatalf("after the run: %d %q (%s)", rec.Code, rec.Body, dir)
	}
	if v, _ := f.view(t, log.id); len(v.Parts) != 1 || v.Parts[0].DurationMS <= 0 {
		t.Fatalf("parts = %+v", v.Parts)
	}
}

func TestServingARecordingAcceptsRanges(t *testing.T) {
	f := newRecordingFixture(t)
	f.rec.size = 100
	id := f.errand(t)
	rec := f.get(f.runURL(id)+"/recording/1", "Range", "bytes=10-19")
	if rec.Code != http.StatusPartialContent || rec.Body.Len() != 10 {
		t.Fatalf("%d, %d bytes", rec.Code, rec.Body.Len())
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 10-19/100" {
		t.Errorf("Content-Range = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Errorf("Cross-Origin-Resource-Policy = %q", got)
	}
	if rec := f.get(f.runURL(id) + "/recording/1"); rec.Code != http.StatusOK || rec.Body.Len() != 100 {
		t.Fatalf("whole: %d", rec.Code)
	}
}

func TestAPartFileIsServedWhenTheRemuxNeverHappened(t *testing.T) {
	f := newRecordingFixture(t)
	log, dir := f.open(t)
	part := strings.TrimSuffix(f.next(t, log), ".mp4") + ".part.mp4"
	if err := os.WriteFile(part, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	log.progress("recording", browser.Message{})
	log.end("ended", "the worker was killed")
	if v, _ := f.view(t, log.id); len(v.Parts) != 1 {
		t.Fatalf("parts = %+v (%s)", v.Parts, dir)
	}
	if rec := f.get(f.runURL(log.id) + "/recording/1"); rec.Code != http.StatusOK || rec.Body.String() != "partial" {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
}

// A crashed engine ends the run when it comes back, however long after: the video ended with its file.
func TestAnUnfinishedPartEndsWhenItsFileLastChanged(t *testing.T) {
	f := newRecordingFixture(t)
	log, _ := f.open(t)
	file := f.next(t, log)
	if err := os.WriteFile(file, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	log.progress("recording", browser.Message{})
	rows, err := f.store.partRows(context.Background(), log.id)
	if err != nil || len(rows) != 1 {
		t.Fatalf("parts = %+v, %v", rows, err)
	}
	if err := os.Chtimes(file, time.Time{}, rows[0].started.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := f.store.EndRun(context.Background(), log.id, rows[0].started.Add(time.Hour), "ended", "the engine restarted"); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.view(t, log.id); len(v.Parts) != 1 || v.Parts[0].DurationMS != 10_000 {
		t.Fatalf("parts = %+v", v.Parts)
	}
}

// What a remux leaves when it is killed is nobody's video: the part it was reading is.
func TestAnUnfinishedRemuxIsNotServed(t *testing.T) {
	f := newRecordingFixture(t)
	log, _ := f.open(t)
	base := strings.TrimSuffix(f.next(t, log), ".mp4")
	if err := os.WriteFile(base+".remux.mp4", []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	log.progress("recording", browser.Message{})
	log.end("ended", "the worker was killed")
	url := f.runURL(log.id) + "/recording/1"
	if v, _ := f.view(t, log.id); len(v.Parts) != 0 {
		t.Fatalf("parts = %+v", v.Parts)
	}
	if rec := f.get(url); rec.Code != http.StatusNotFound {
		t.Fatalf("alone: %d", rec.Code)
	}
	if err := os.WriteFile(base+".part.mp4", []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := f.get(url); rec.Code != http.StatusOK || rec.Body.String() != "partial" {
		t.Fatalf("beside the part: %d %q", rec.Code, rec.Body)
	}
}

func TestAnotherSitesRunGets404(t *testing.T) {
	f := newRecordingFixture(t)
	id := f.errand(t)
	for _, path := range []string{
		fmt.Sprintf("/api/v1/signins/sites/other.test/runs/%d/recording/1", id),
		f.runURL(id) + "/recording/3",
		f.runURL(id) + "/recording/x",
		f.runURL(id) + "/recording/-1",
		"/api/v1/signins/sites/shop.test/runs/x/recording/1",
		"/api/v1/signins/sites/shop.test/runs/999/recording/1",
	} {
		if rec := f.get(path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	if rec := f.get(f.runURL(id) + "/recording/1"); rec.Code != http.StatusOK {
		t.Fatalf("own run: %d", rec.Code)
	}
}

// The engine reads the site's run directories and asks the worker to forget the runs its store no
// longer keeps; it deletes nothing itself.
func TestPruneAsksTheWorkerForDroppedRuns(t *testing.T) {
	f := newRecordingFixture(t)
	ctx := context.Background()
	// The worker deletes what it is asked to.
	f.worker.onForget = func(typ string, body browser.Message) {
		if typ == "forget_recordings" {
			for _, run := range body["runs"].([]int64) {
				os.RemoveAll(runDir(f.dir, body["site"].(string), run))
			}
		}
	}
	first, err := f.svc.startRun(ctx, shopKey, "errand")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{runDir(f.dir, shopKey, first.id), runDir(f.dir, shopKey, 9999),
		filepath.Join(f.dir, shopKey, "notarun"), runDir(f.dir, "other.test", 1)} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for range keptRuns {
		if _, err := f.svc.startRun(ctx, shopKey, "errand"); err != nil {
			t.Fatal(err)
		}
	}
	kept, err := f.store.Runs(ctx, shopKey)
	if err != nil {
		t.Fatal(err)
	}
	keptIDs := map[int64]bool{}
	for _, r := range kept {
		keptIDs[r.ID] = true
	}
	forgotten := map[int64]bool{}
	for _, m := range f.worker.forgot() {
		if m["type"] != "forget_recordings" || m["site"] != shopKey {
			t.Fatalf("asked %v", m)
		}
		for _, run := range m["runs"].([]int64) {
			if keptIDs[run] {
				t.Errorf("a kept run %d was named", run)
			}
			forgotten[run] = true
		}
	}
	if !reflect.DeepEqual(forgotten, map[int64]bool{first.id: true, 9999: true}) {
		t.Fatalf("forgotten = %v (first %d)", forgotten, first.id)
	}
	if _, err := os.Stat(runDir(f.dir, "other.test", 1)); err != nil {
		t.Errorf("another site's recording went: %v", err)
	}
}

// Its runs stay in the Errands list, videos and all.
func TestRemovingAnAccountKeepsItsRecordings(t *testing.T) {
	f := newRecordingFixture(t)
	f.signedUp(t, "registered")
	if err := os.MkdirAll(filepath.Join(f.dir, shopKey, "1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RemoveAccount(context.Background(), shopKey); err != nil {
		t.Fatal(err)
	}
	for _, m := range f.worker.forgot() {
		if m["type"] != "forget_profile" {
			t.Errorf("asked %v", m)
		}
	}
}

// A key that isn't one is never sent to the worker to forget: nothing outside its roots is named.
func TestTheEngineNeverAsksToForgetANonKey(t *testing.T) {
	f := newRecordingFixture(t)
	ctx := context.Background()
	for _, key := range []string{"../x", "a/b", "", ".", ".."} {
		if err := f.svc.forgetProfile(ctx, key); err == nil {
			t.Errorf("forgetProfile(%q) = nil", key)
		}
		f.svc.pruneRecordings(ctx, key)
		if err := f.svc.RemoveAccount(ctx, key); err == nil {
			t.Errorf("RemoveAccount(%q) = nil", key)
		}
	}
	if got := f.worker.forgot(); len(got) != 0 {
		t.Fatalf("asked %v", got)
	}
}

func TestAClickIsKept(t *testing.T) {
	f := newRecordingFixture(t)
	log, _ := f.open(t)
	log.progress("action", browser.Message{"kind": "click", "target": "Entrar", "result": "ok", "x": float64(640), "y": float64(360)})
	log.progress("action", browser.Message{"kind": "click", "target": "Entrar", "result": "refused"})
	log.progress("action", browser.Message{"kind": "scroll", "result": "ok"})
	v, raw := f.view(t, log.id)
	if len(v.Clicks) != 1 || v.Clicks[0].X != 640 || v.Clicks[0].Y != 360 || v.Clicks[0].At.IsZero() {
		t.Fatalf("clicks = %s", raw)
	}
	if !strings.Contains(raw, `"parts":[]`) {
		t.Errorf("parts are not an empty list: %s", raw)
	}
}

func TestAFailedRecordingIsSaidInTheLogAndCut(t *testing.T) {
	f := newRecordingFixture(t)
	log, _ := f.open(t)
	log.progress("recording_failed", browser.Message{"detail": "ffmpeg quit: " + strings.Repeat("é", 300)})
	_, lines := lineTexts(t, f.store, shopKey, log.id)
	want := "box: This part of the run wasn't recorded: ffmpeg quit: " + strings.Repeat("é", 200-len("ffmpeg quit: ")) + "…"
	if !contains(lines, want) {
		t.Fatalf("lines = %q", lines)
	}
}
