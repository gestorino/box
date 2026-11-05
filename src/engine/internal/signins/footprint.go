package signins

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"

	"concierge/engine/internal/browser"
)

// A footprint is the page a model sign-in proved the box signed in on: a link and the clicks after
// it, by the controls' visible names. A check reads it without the model (the worker's
// read_signed_in): the account's own details there are signed in, a password field signed out, a
// human check in the way the owner's, anything else broken; only then does the model sign in, and the
// worker proves the model's path in its own browser before it closes: that is the new footprint.
// Design: docs/superpowers/specs/2026-11-03-sign-in-footprint-design.md.

const (
	readSignedIn   = "signed_in"
	readSignedOut  = "signed_out"
	readHumanCheck = "human_check"
)

// foundWords names the kind of detail a read found, never the detail.
var foundWords = map[string]string{"email": "your email", "name": "your name", "phone": "the box's phone"}

// readPage reads the footprint in the account's profile without the model: signed_in, signed_out, human_check or broken,
// and the kind of detail found. err is a read that didn't happen (Stop, the engine stopping, the
// worker away), which says nothing of the page. The details go to the worker to look for and are
// never logged.
func (s *Service) readPage(ctx context.Context, log *runLog, a Account) (result, found string, err error) {
	request := browser.Message{"site": a.Key, "session": a.Key, "url": a.Footprint, "clicks": a.FootprintClicks,
		"allowed_domains": []string{a.Key, "*." + a.Key}, "proofs": s.proofs(ctx, a)}
	maps.Copy(request, log.record())
	reply, err := s.cfg.Worker.RequestAsk(ctx, "read_signed_in", request, nil, log.progress)
	log.partEnded()
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		slog.Warn("signins: reading a saved page", "site", a.Key, "run", log.id, "err", err)
		return "", "", err
	}
	result, _ = reply["result"].(string)
	found, _ = reply["found"].(string)
	return result, found, nil
}

// proofs are the account's details a page is read for: looked for, never logged.
func (s *Service) proofs(ctx context.Context, a Account) map[string]string {
	return map[string]string{"email": a.Email, "first_name": a.Owner["first_name"], "last_name": a.Owner["last_name"],
		"phone": s.phone(ctx)}
}

// footprintOf is the page a footprint keeps: its origin and path. A query or fragment may carry a
// token or a detail of the account; "" for an address that isn't an http(s) page.
func footprintOf(page string) string {
	u, err := url.Parse(page)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

// pathOf is what a log line says of a footprint: its path, and how many clicks follow (never their
// names: one may be the owner's).
func pathOf(footprint string, clicks []string) string {
	path := ""
	if u, err := url.Parse(footprint); err == nil {
		path = cmp.Or(u.Path, "/")
	}
	return path + clicksAfter(len(clicks))
}

// clicksAfter is " after N clicks", "" for none.
func clicksAfter(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return " after 1 click"
	default:
		return fmt.Sprintf(" after %d clicks", n)
	}
}

// readFootprint is a check's free part: the account's footprint read without the model. Signed in
// ends the run there, with no model used; so does a read that didn't happen (Stop, the engine
// stopping, the worker away), the way an errand's end says it. True when the run is over.
func (s *Service) readFootprint(ctx context.Context, e *agentRun, log *runLog, a Account) bool {
	log.box("Reading the saved page %s", pathOf(a.Footprint, a.FootprintClicks))
	result, found, err := s.readPage(ctx, log, a)
	end := func(state, reason, report string) bool {
		log.spent(nil, nil)
		if err := s.cfg.Store.FinishAgentRun(context.WithoutCancel(ctx), log.id, report, nil); err != nil {
			slog.Error("signins: storing a check's result", "run", log.id, "err", err)
		}
		log.end(state, reason)
		return true
	}
	if err != nil {
		s.mu.Lock()
		ended := e.ended
		s.mu.Unlock()
		switch {
		case ended != "":
			return end("ended", ended, "")
		case ctx.Err() != nil:
			return end("ended", "the box restarted", "")
		case errors.Is(err, browser.ErrUnavailable):
			return end("ended", "the browser worker was away", "")
		default:
			return end("warn", "the saved page could not be read", "")
		}
	}
	switch result {
	case readSignedIn:
		log.box("Read the saved page: signed in (%s)", foundWords[found])
		return end("ended", "signed in", "Signed in: the saved page shows "+foundWords[found]+".")
	case readSignedOut:
		log.box("Read the saved page: signed out: the site asks to sign in")
	case readHumanCheck:
		log.box("Read the saved page: a human check is in the way")
	default:
		log.box("Read the saved page: it shows neither")
	}
	return false
}

// keepFootprint stores what the worker proved in the model's own browser once the model signed in:
// the link (its origin and path only) and the clicks after it, the old footprint among what it
// tried. Nothing proved: no footprint, so the site never claims free checks it can't do.
func (s *Service) keepFootprint(ctx context.Context, log *runLog, key string, proved any) {
	fp, _ := proved.(map[string]any)
	page, _ := fp["url"].(string)
	page = footprintOf(page)
	var clicks []string
	if raw, ok := fp["clicks"].([]any); ok {
		for _, c := range raw {
			if name, ok := c.(string); ok && name != "" {
				clicks = append(clicks, name)
			}
		}
	}
	if page == "" {
		clicks = nil
		log.box("No page the box could open again showed the account: no footprint kept")
	} else if len(clicks) > 0 {
		log.box("Kept this page as the sign-in footprint,%s", clicksAfter(len(clicks)))
	} else {
		log.box("Kept this page as the sign-in footprint")
	}
	if err := s.cfg.Store.SetFootprint(context.WithoutCancel(ctx), key, page, clicks); err != nil {
		slog.Error("signins: storing a footprint", "site", key, "err", err)
	}
}

// provedWords is how a proving try read, in the owner's words.
var provedWords = map[string]string{readSignedIn: "it shows the account", readSignedOut: "signed out",
	readHumanCheck: "a human check is in the way", "broken": "it shows neither"}

// provingLine is the run-log line of one try the worker made to prove a footprint (its `prove`
// event): how many clicks and how it read, never the link or the names.
func provingLine(p browser.Message) string {
	clicks, _ := p["clicks"].(float64)
	result, _ := p["result"].(string)
	what := provedWords[result]
	if result == "error" {
		kind, _ := p["error"].(string)
		what = "it could not be opened (" + kind + ")"
	}
	where := "the page it ended on"
	if n := int(clicks); n > 0 {
		where = "it" + clicksAfter(n)
	}
	return fmt.Sprintf("Tried %s without the model: %s", where, cmp.Or(what, result))
}
