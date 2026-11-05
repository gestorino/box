package signins

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"concierge/engine/internal/browser"
)

// The catalog grows only through the API: Add asks for a suggested card (Suggest), which the owner
// edits and saves (CreateCard); a card can be edited later (UpdateCard). Design:
// docs/superpowers/specs/2026-11-03-catalog-actions-design.md.

// ErrBadCard refuses a link or a card the console couldn't show: its reason is the owner's to read.
var ErrBadCard = errors.New("not a card")

// ErrCardExists refuses creating a card of a site the catalog already holds.
var ErrCardExists = errors.New("the catalog already holds this site")

// suggestWait bounds reading a site's page and suggesting its actions (one model call).
const suggestWait = 60 * time.Second

func badCard(why string) error { return fmt.Errorf("%w: %s", ErrBadCard, why) }

// Suggestion is the card Add suggests for a link, not stored; Exists when the site already has a
// card (only Key then: the console opens that card's edit page). ActionsError says why there are no
// actions when there are none.
type Suggestion struct {
	Key          string   `json:"key"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Link         string   `json:"link"`
	Icon         string   `json:"icon"`
	Actions      []Action `json:"actions"`
	ActionsError string   `json:"actions_error,omitempty"`
	// ActionsNote says where the actions come from when not from the page: what the model knows of
	// the site, its page unread (a bot wall).
	ActionsNote string `json:"actions_note,omitempty"`
	Exists      bool   `json:"exists,omitempty"`
}

// CardInput is a card as the owner saves it.
type CardInput struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Link        string   `json:"link"`
	Icon        string   `json:"icon"`
	Actions     []Action `json:"actions"`
}

// linkKey is a link's site key: an http(s) URL of a registrable domain.
func linkKey(link string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return "", "", badCard("the link must be an http(s) address of a site")
	}
	key, err := siteKey(u.Hostname())
	if err != nil || !isKey(key) {
		return "", "", badCard("the link must be an http(s) address of a site")
	}
	return key, u.String(), nil
}

// Suggest reads the site at link for a card: name, description and icon off its page, and the
// actions a model suggests from its text (the worker's site_card). Nothing is stored. A page that
// can't be read still suggests the site, with nothing read off it.
func (s *Service) Suggest(ctx context.Context, link string) (Suggestion, error) {
	key, _, err := linkKey(link)
	if err != nil {
		return Suggestion{}, err
	}
	if _, found, err := s.cfg.Store.Card(ctx, key); err != nil {
		return Suggestion{}, err
	} else if found {
		return Suggestion{Key: key, Exists: true, Actions: []Action{}}, nil
	}
	return s.SuggestAgain(ctx, link)
}

// SuggestAgain is the edit page's Read the page again: a fresh suggestion from the link whether or
// not its site has a card. Nothing is stored.
func (s *Service) SuggestAgain(ctx context.Context, link string) (Suggestion, error) {
	key, link, err := linkKey(link)
	if err != nil {
		return Suggestion{}, err
	}
	// Name, description and icon are what the page says, "" when it says nothing: a new card is named
	// by its key in the console, an edit keeps its own.
	sg := Suggestion{Key: key, Link: link, Actions: []Action{}}
	read, cancel := context.WithTimeout(ctx, suggestWait)
	defer cancel()
	reply, err := s.cfg.Worker.RequestAsk(read, "site_card", browser.Message{"url": link}, nil, suggestSpent(key))
	if err != nil {
		slog.Warn("signins: the site's page could not be read for its card", "site", key, "err", err)
		sg.ActionsError = "the page could not be read"
		return sg, nil
	}
	if name, _ := reply["name"].(string); oneLine(name) != "" {
		sg.Name = cut(oneLine(name), nameMax)
	}
	description, _ := reply["description"].(string)
	sg.Description = cut(oneLine(description), descriptionMax)
	if icon, _ := reply["icon"].(string); icon != "" {
		if _, err := iconPNG(icon); err == nil {
			sg.Icon = icon
		}
	}
	raw, _ := reply["actions"].([]any)
	var suggested []Action
	for _, a := range raw {
		m, _ := a.(map[string]any)
		text, _ := m["text"].(string)
		var params []string
		ps, _ := m["params"].([]any)
		for _, p := range ps {
			if p, ok := p.(string); ok {
				params = append(params, p)
			}
		}
		effect, _ := m["effect"].(string)
		suggested = append(suggested, Action{Text: text, Params: params, Effect: effect})
	}
	if actions, err := cardActions(suggested); err == nil {
		sg.Actions = actions
	}
	sg.ActionsError, _ = reply["actions_error"].(string)
	sg.ActionsNote, _ = reply["actions_note"].(string)
	return sg, nil
}

// suggestSpent logs what the suggestion's model call cost: there is no run to count it on.
func suggestSpent(key string) browser.Progress {
	return func(event string, p browser.Message) {
		if event == "model_call" {
			slog.Info("signins: a card's actions were suggested", "site", key, "model", p["model"],
				"provider", p["provider"], "cost_usd", p["cost_usd"])
		}
	}
}

// cardActions is the owner's actions as kept: words and details one line each, trimmed, empty ones
// and repeats (any case) dropped, an effect not said none; refused past actionsMax actions of
// actionMax characters, paramsMax details of paramMax characters, or an effect there is none of.
func cardActions(in []Action) ([]Action, error) {
	out := []Action{}
	seen := map[string]bool{}
	for _, a := range in {
		text, err := kept(a.Text, actionMax, "an action")
		if err != nil {
			return nil, err
		}
		if text == "" || seen[strings.ToLower(text)] {
			continue
		}
		seen[strings.ToLower(text)] = true
		params := []string{}
		said := map[string]bool{}
		for _, p := range a.Params {
			p, err := kept(p, paramMax, "a detail")
			if err != nil {
				return nil, err
			}
			if p == "" || said[strings.ToLower(p)] {
				continue
			}
			said[strings.ToLower(p)] = true
			params = append(params, p)
		}
		if len(params) > paramsMax {
			return nil, badCard(fmt.Sprintf("an action has at most %d details", paramsMax))
		}
		effect := cmp.Or(a.Effect, "none")
		if !effects[effect] {
			return nil, badCard("an action's effect is none, create, change, cancel or sync")
		}
		out = append(out, Action{Text: text, Params: params, Effect: effect})
	}
	if len(out) > actionsMax {
		return nil, badCard(fmt.Sprintf("a card has at most %d actions", actionsMax))
	}
	return out, nil
}

// kept is one line of the owner's words, trimmed; refused when on several lines or past most.
func kept(s string, most int, what string) (string, error) {
	if strings.ContainsAny(s, "\r\n") {
		return "", badCard(what + " is one line")
	}
	s = oneLine(s)
	if len([]rune(s)) > most {
		return "", badCard(fmt.Sprintf("%s is at most %d characters", what, most))
	}
	return s, nil
}

// EditableCard is the card of key as its edit page fills it, its icon included; found is false for
// a site with no card.
func (s *Service) EditableCard(ctx context.Context, key string) (CardInput, bool, error) {
	c, found, err := s.cfg.Store.Card(ctx, key)
	if err != nil || !found {
		return CardInput{}, false, err
	}
	return inputOf(c), true, nil
}

// inputOf is a kept card as the edit page fills it.
func inputOf(c Card) CardInput {
	return CardInput{Name: c.Name, Description: c.Description, Link: c.Link, Icon: c.Icon, Actions: c.Actions}
}

// cardOf is the card the owner's input makes, checked as the console shows it.
func cardOf(in CardInput) (Card, error) {
	key, link, err := linkKey(in.Link)
	if err != nil {
		return Card{}, err
	}
	name := oneLine(in.Name)
	if name == "" || len([]rune(name)) > nameMax {
		return Card{}, badCard(fmt.Sprintf("a card needs a name of at most %d characters", nameMax))
	}
	if strings.ContainsAny(in.Description, "\r\n") || len([]rune(in.Description)) > descriptionMax {
		return Card{}, badCard(fmt.Sprintf("the description is one line of at most %d characters", descriptionMax))
	}
	if _, err := iconPNG(in.Icon); err != nil {
		return Card{}, badCard(err.Error())
	}
	actions, err := cardActions(in.Actions)
	if err != nil {
		return Card{}, err
	}
	return Card{Key: key, Name: name, Description: strings.TrimSpace(in.Description), Link: link, Icon: in.Icon,
		Actions: actions}, nil
}

// CreateCard keeps the owner's new card; a site with a card already is refused (ErrCardExists).
func (s *Service) CreateCard(ctx context.Context, in CardInput) (SiteView, error) {
	c, err := cardOf(in)
	if err != nil {
		return SiteView{}, err
	}
	c.AddedAt = s.cfg.Now()
	added, err := s.cfg.Store.InsertCard(ctx, c)
	if err != nil {
		return SiteView{}, err
	}
	if !added {
		return SiteView{}, ErrCardExists
	}
	return s.siteView(ctx, c)
}

// UpdateCard writes the owner's edit of the card of key: its link may move within the site, never to
// another (ErrBadCard).
func (s *Service) UpdateCard(ctx context.Context, key string, in CardInput) (SiteView, error) {
	old, found, err := s.cfg.Store.Card(ctx, key)
	if err != nil {
		return SiteView{}, err
	}
	if !found {
		return SiteView{}, ErrUnknownSite
	}
	c, err := cardOf(in)
	if err != nil {
		return SiteView{}, err
	}
	if c.Key != key {
		return SiteView{}, badCard("the link must stay on the card's own site")
	}
	c.AddedAt = old.AddedAt
	if err := s.cfg.Store.UpdateCard(ctx, c); err != nil {
		return SiteView{}, err
	}
	return s.siteView(ctx, c)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
