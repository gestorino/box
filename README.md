# Concierge

Concierge runs on a small Linux box at home with its own SIM. It does errands on websites for its
owner: given a link and what to do there ("book a table"), an agent drives a real Chrome on the
box's own screen, asks the owner every choice the site offers and confirms before the step that
finally does it. Payment, destructive and captcha controls are refused before they reach the page;
captchas go to the owner. It can register and keep the box's own accounts on sites, using the SIM
and mailbox for their codes, and remembers the appointments its errands made.

It also keeps the owner's SMS, calls, email, WhatsApp and Telegram in one place, each archived by its
own small daemon under its own user, and sends only what the owner sends from the console. A Go
engine serves the console (React) and supervises a Python browser-use worker; secrets live in a
separate service sealed with a TPM-held key. [CLAUDE.md](CLAUDE.md) describes the layout and how
each piece works; `docs/` holds the specs and designs.

## Build and test

```sh
make check   # Go engine + Python worker + UI: the merge bar, also run by CI on every branch push
```

You need Go (the version `src/engine/go.mod` pins), [uv](https://docs.astral.sh/uv/) and Node.
Tests never touch the network. `make run` starts the engine, worker and UI locally on
127.0.0.1:8080.

## Releases

`make release` moves `VERSION` to the next minor (`0.1.x` → `0.2.0`), commits and tags it locally,
and prints the two pushes (`main`, then the tag). A pushed `vX.Y.0` tag makes CI run `make check`,
build `concierge-<version>-linux-amd64.tar.gz` (the nine daemons for linux/amd64, the UI build and
the worker's source) and a `manifest.json` of every file's SHA-256, attest both with GitHub's build
provenance and publish them as the tag's release. Any other tag publishes nothing.

To check that a download was built by this repository's release workflow:

```sh
gh attestation verify concierge-0.2.0-linux-amd64.tar.gz --repo telepenin/concierge \
  --signer-workflow telepenin/concierge/.github/workflows/release.yml
```

## License

MIT, see [LICENSE](LICENSE).
