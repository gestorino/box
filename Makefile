# The merge bar: `make check` must be green before every commit.
.PHONY: check generate test-engine test-browser test-browser-live test-agent-live test-box check-ui test-release test-dist dist release run deploy box-browser

check: test-engine test-browser check-ui test-release

# Regenerate the listen and agent contracts' Go code (offline: the generators are pinned `tool`s in their own
# module, src/engine/tools, so they never move the versions the engine's binaries ship).
generate:
	cd src/engine && go tool -modfile=tools/go.mod buf generate

# Go engine: generated code is fresh, vet + tests under the race detector.
test-engine: generate
	@test -z "$$(git status --porcelain src/engine/internal/gen)" || { echo "generated code is stale: run make generate and commit"; exit 1; }
	cd src/engine && go vet ./... && go test -race ./...

# Python browser-use worker, hermetic tier (no Chromium).
test-browser:
	cd src/browser && uv run --with pytest --with-editable . pytest -q -m 'not live and not box'

# Opt-in: real Chromium against the loopback fixture. Not part of `check`.
test-browser-live:
	cd src/browser && uv run --with pytest --with-editable . pytest -q -m live

# Opt-in: one real Step through claudd and codexd, with each daemon's home (CLAUDD_HOME, CODEXD_HOME) already signed in through that daemon
# (`make test-agent-live ARGS=--record` saves the raw replies over the testdata). Not part of `check`.
test-agent-live:
	scripts/agent-live.sh $(ARGS)

# Opt-in: the screen (Xvnc, openbox, websockify, Chrome) on the real box, over SSH. Not part of `check`.
# The box is $CONCIERGE_BOX (its ssh target; no default: the owner exports it as CLAUDE.local.md writes it).
test-box:
	CONCIERGE_BOX="$(CONCIERGE_BOX)" scripts/box-test.sh

# UI: install from the lockfile, tests, typecheck, build.
check-ui:
	cd src/ui && npm ci --silent && npm test && npm run build

# The release rules (which tag publishes; what `make release` does; what the pre-push hook lets
# out), in throwaway git repos.
test-release:
	scripts/test-release.sh

# The release archive, built for real and checked. Not in `check` (it builds everything); CI runs
# it after `make check`.
test-dist:
	scripts/test-dist.sh

# The release archive: dist/concierge-$(V)-linux-amd64.tar.gz and dist/manifest.json, e.g.
#   make dist V=0.2.0 R=$$(git rev-parse --short HEAD)
dist:
	scripts/dist.sh "$(V)" "$(R)"

# Cut a public release: VERSION to the next minor, committed and tagged on main. Prints the two
# pushes and pushes nothing; CI publishes the tag once it is pushed. Only a green tree whose
# release archive builds right is tagged.
release: check test-dist
	scripts/cut-release.sh

# Engine + worker + built UI on 127.0.0.1:8080. Console login for local runs: dev / dev.
run: check-ui
	@set -e; d="$${TMPDIR:-/tmp}/concierge-dev/secretd"; mkdir -p "$$d"; chmod 700 "$$d"; \
	w="$${TMPDIR:-/tmp}/concierge-dev/worker"; mkdir -p "$$w"; chmod 700 "$$w"; \
	if [ ! -f "$$d/master.key" ]; then (umask 077; head -c 32 /dev/urandom > "$$d/master.key"); fi; \
	[ $$(printf %s "$$d/s" | wc -c) -lt 100 ] || { echo "secretd socket path too long: $$d/s" >&2; exit 1; }; \
	[ $$(printf %s "$$w/w" | wc -c) -lt 100 ] || { echo "worker socket path too long: $$w/w" >&2; exit 1; }; \
	(cd src/engine && go build -o "$$d/secretd" ./cmd/secretd); \
	"$$d/secretd" -caller engine=$(USER) -dir "$$d" -master-key-file "$$d/master.key" -listen "$$d/s" & \
	secretd=$$!; \
	(cd src/browser && exec uv run python -m concierge_worker --listen "$$w/w" --profiles "$$w/profiles") & \
	worker=$$!; \
	trap 'kill $$secretd $$worker 2>/dev/null' EXIT INT TERM; \
	cd src/engine; \
	CONCIERGE_CONSOLE_USER=dev CONCIERGE_CONSOLE_PASSWORD_HASH='$$2y$$10$$JrPUgGvnCvBkl51b71Jm9uRGyIyF8GqOxS3gxC/89ayCH3KNd.bie' \
		go run ./cmd/engine -worker-socket "$$w/w" -ui-dir ../ui/dist -secrets-socket "$$d/s"

# Build and ship to the box (ansible/, a private repo: its README.md), only after the merge bar is green. The
# playbook refuses anything but a clean main in the primary checkout.
deploy: check
	cd ansible && ansible-playbook deploy.yml --tags deploy

# A plain Chrome on the box ($CONCIERGE_BOX), driven by hand in the browser (noVNC); FLAGS go to Chrome.
# Ctrl+C ends it.
box-browser:
	CONCIERGE_BOX="$(CONCIERGE_BOX)" scripts/box-browser.sh $(FLAGS)
