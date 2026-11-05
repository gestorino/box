package calls

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestTurnsAreServed(t *testing.T) {
	g := newRig(t)
	id, _ := g.a.Start(ctx, "+351911111111", "outgoing", t0)
	quiet, _ := g.a.Start(ctx, "+351922222222", "outgoing", t0)
	want := []Turn{{Speaker: "box", Text: "What is 1 plus 1?", At: ts(t0)}, {Speaker: "them", Text: "two", At: ts(t0)}}
	g.a.AddTurns(ctx, id, want)
	h := NewHandler(ctx, g.a, nil, noKey(t), voiceUnknown, ownerUnknown)

	var got []Turn
	rec := get(h, "/calls/"+itoa(id)+"/turns")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != 200 || err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := get(h, "/calls/"+itoa(quiet)+"/turns"); rec.Code != 200 || rec.Body.String() != "[]\n" {
		t.Fatalf("no turns must be []: %d %q", rec.Code, rec.Body)
	}
	for _, bad := range []string{"999", "0", "-1", "abc", "1.5"} {
		if rec := get(h, "/calls/"+bad+"/turns"); rec.Code != 404 {
			t.Fatalf("%s: %d", bad, rec.Code)
		}
	}
}

func recordAudio(t *testing.T, g *rig, body string) int64 {
	t.Helper()
	id, _ := g.a.Start(ctx, "+351911111111", "outgoing", t0)
	if err := os.MkdirAll(filepath.Dir(g.a.AudioPath(id)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.a.AudioPath(id), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAudioIsServedAsWav(t *testing.T) {
	g := newRig(t)
	id := recordAudio(t, g, "RIFFdata")
	rec := get(NewHandler(ctx, g.a, nil, noKey(t), voiceUnknown, ownerUnknown), "/calls/"+itoa(id)+"/audio")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "audio/wav" || rec.Body.String() != "RIFFdata" {
		t.Fatalf("%d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	if d := rec.Header().Get("Content-Disposition"); d != "" {
		t.Fatalf("inline playback must carry no disposition: %q", d)
	}
}

func TestDownloadAddsADisposition(t *testing.T) {
	g := newRig(t)
	id := recordAudio(t, g, "RIFFdata")
	rec := get(NewHandler(ctx, g.a, nil, noKey(t), voiceUnknown, ownerUnknown), "/calls/"+itoa(id)+"/audio?download=1")
	want := `attachment; filename="call-` + itoa(id) + `.wav"`
	if rec.Code != 200 || rec.Header().Get("Content-Disposition") != want {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
}

func TestNoAudioIs404(t *testing.T) {
	g := newRig(t)
	id, _ := g.a.Start(ctx, "+351911111111", "outgoing", t0)
	h := NewHandler(ctx, g.a, nil, noKey(t), voiceUnknown, ownerUnknown)
	for _, p := range []string{"/calls/" + itoa(id) + "/audio", "/calls/999/audio", "/calls/abc/audio"} {
		if rec := get(h, p); rec.Code != 404 {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func TestSeenEndpoint(t *testing.T) {
	g := newRig(t)
	g.a.Start(ctx, "+351911111111", "incoming", t0)
	h := NewHandler(ctx, g.a, nil, noKey(t), voiceUnknown, ownerUnknown)
	count := func() int {
		var out struct{ Count int }
		rec := get(h, "/calls/unread")
		if err := json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != 200 || err != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return out.Count
	}
	if n := count(); n != 1 {
		t.Fatalf("unread %d", n)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/calls/seen", nil))
	if rec.Code != 204 {
		t.Fatalf("seen: %d %s", rec.Code, rec.Body)
	}
	if n := count(); n != 0 {
		t.Fatalf("unread %d", n)
	}
}

// A transcript or recording must not linger in the browser's cache once the owner deletes it, nor
// be sniffed into something the browser runs.
func TestTurnsAndAudioAreNeverCached(t *testing.T) {
	g := newRig(t)
	id := recordAudio(t, g, "RIFFdata")
	g.a.AddTurns(ctx, id, []Turn{{Speaker: "them", Text: "hi", At: ts(t0)}})
	h := NewHandler(ctx, g.a, nil, noKey(t), voiceUnknown, ownerUnknown)
	for _, p := range []string{"/turns", "/audio", "/audio?download=1"} {
		rec := get(h, "/calls/"+itoa(id)+p)
		if rec.Code != 200 {
			t.Fatalf("%s: %d", p, rec.Code)
		}
		if c := rec.Header().Get("Cache-Control"); c != "no-store" {
			t.Fatalf("%s: Cache-Control %q, want no-store", p, c)
		}
		if n := rec.Header().Get("X-Content-Type-Options"); n != "nosniff" {
			t.Fatalf("%s: X-Content-Type-Options %q, want nosniff", p, n)
		}
	}
}
