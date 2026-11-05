# `src/browser/` — the browser worker, in Python

The engine's only browser executor: a service of its own the Go engine (`src/engine/`) connects to,
which drives the page with [browser-use](https://github.com/browser-use/browser-use) over CDP.

**We adopted their hands and kept our rules.** Every policy in here is ours and runs *in front of*
their agent loop, never beside it.

## How it speaks to the engine

Newline-delimited JSON over a Unix domain socket the worker binds (mode 0660) and the engine
connects to, one engine at a time (`ipc.listen`). The contract's two halves are
`concierge_worker/ipc.py` here and `src/engine/internal/browser/worker.go`; change them together.
The box's `concierge-worker` unit (and `make run`, locally) starts it:

```
python -m concierge_worker --listen SOCK --profiles DIR [--screens DIR [--recordings DIR]]
```

Every file it writes is under those directories, at paths it computes itself (`files.py`): a site's
profile, a run's screen, and a run's videos and log (`<recordings>/<site>/<run>/`, at most 100 MiB a
run). The engine reads the recordings and never writes them; it asks the worker to delete a profile
(`forget_profile`) or old runs' recordings (`forget_recordings`).

Every model call runs on the owner's OpenRouter key, set on the console's AI page. The engine keeps
it, and puts it in each request frame that runs a model (`site_card`, `read_arranged`, `run_agent`)
as `openrouter_key`. The worker pops it out of the frame as the request starts
(`concierge_worker/keys.py`), keeps no file or copy, and has no fallback to the environment. A run
without a key fails before its browser starts, with the error `no OpenRouter key`.

Locally, `make run` starts it with `uv run`, which resolves `pyproject.toml` + `uv.lock` into this directory's own
environment (`uv sync --directory src/browser` once first; without it the first start pays for the download,
~108 packages, ~230 MB). On the box the unit runs the root-owned venv's python directly: no `uv` at run time.

## What to read first

| What | Where | The one-line rule |
|---|---|---|
| **The agent** | `concierge_worker/agent.py` | One browser-use agent on general rules does an action at a link, by its run's kind (`PROMPTS`): an errand asks the owner every choice (`ask_user`) and once more before the press that does it; a registration or a sign-in presses it without asking; on the owner's account it fills their values by name and fetches the box's codes and emails. |
| **The gate** | `concierge_worker/gate.py` | `register_new_step_callback` fires *before* `_execute_actions`, so a payment, captcha or account-deleting control is dropped from the batch rather than reported after the fact. A watchdog that raises runs alongside the handler — the spike's veto fired and the card number was typed anyway. |
| **The fill table** | `concierge_worker/fill.py` | `fill_field` is the only way a value enters a page. The model names a field, never holds a value, and gets a placeholder back — so the action space, every parameter and the whole history carry names only. browser-use's own `sensitive_data` is used nowhere. |
| **Containment** | `concierge_worker/browsing.py` (`forbidden_navigation`) | Their allowlist reports violations rather than preventing them and lets `data:`/`blob:` past before it is consulted: a navigation the model asks for off the site's domains is dropped before the request leaves. |
| **A site's card** | `concierge_worker/card.py` | The page's title, description and icon for the owner's Add — one plain fetch, no browser, no model, nothing invented. |

## Running its tests

```bash
make test-browser        # hermetic: no network, no Chromium, no API key. Part of `make check`.
make test-browser-live   # real Chromium against the loopback fixture (`pytest -m live`).
cd src/browser && uv run --with pytest --with-editable . pytest -q tests/test_gate.py   # one file
```

The first is part of the per-commit merge bar; the second must be green before a feature branch that touches it merges. `-m live`
is enforced by `--strict-markers`, so a mistyped marker is a collection error rather than a Chromium
launch on the fast tier.

Designs: `docs/superpowers/specs/2026-11-03-errands-design.md`, `2026-11-03-registration-as-errand-design.md`.
