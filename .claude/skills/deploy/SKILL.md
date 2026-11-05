---
name: deploy
description: Use after ANY feature, fix or change has been merged into local main, and whenever asked to deploy, ship, update or refresh the box (the mini-PC in ansible/inventory.yml). Merges nothing itself; builds and ships the concierge with Ansible from a clean main in the primary checkout, then proves the box runs local main's HEAD.
---

# Deploy to the mini-PC

The box (the one host in `ansible/inventory.yml`; its address and SSH target are also in
`CLAUDE.local.md`, which writes out `CONCIERGE_BOX` for the owner to export) must always run what local `main` holds. Deploy after every change that
lands on main — a feature, a fix, a docs-only change included (it costs nothing: an unchanged
artifact restarts nothing).

## Never

- **Never deploy a branch or a worktree.** Finish the work, merge it into `main` locally in the
  primary checkout, then deploy from there. The playbook refuses anything else; do not get round
  that refusal — no `--skip-tags`, no editing the preflight task, no `ansible-playbook` run from a
  worktree path, no copying files to the box by hand.
- **Never deploy past a red merge bar.** `make deploy` runs `make check` first; if it fails, fix the
  code, commit, and start again.

## Steps

1. **Be in the primary checkout on main.** `git worktree list` — the first line is the primary
   checkout. If you worked in a worktree or on a branch, merge it there first:

   ```bash
   cd <primary checkout>
   git switch main
   git merge --ff-only <branch>     # or a regular merge if main moved on
   ```

   Merging is a local step the user has asked for as part of this flow; pushing is not — never push
   unless asked.

2. **Deploy:**

   ```bash
   make deploy
   ```

   That is `make check`, then `ansible-playbook deploy.yml --tags deploy`. **Every deploy ships a
   version** (`ansible/release.sh`): a new commit gets the next patch number — `VERSION` is bumped,
   committed on main as `chore(release): vX.Y.Z` and tagged `vX.Y.Z`; re-deploying an already
   tagged commit keeps its number. Patch versions come only from deploys; a minor is
   `make release` (`VERSION` to the next X.Y.0, committed and tagged `vX.Y.0`, a public release
   tag), then deploy. Deploys' patch tags stay local — never push them (the pre-push hook refuses
   them). A fresh box, or a change
   to host setup (packages, uv version, the service user), needs the full run instead:
   `cd ansible && ansible-playbook deploy.yml`. The playbook's last task already waits until
   `/api/v1/status` reports the browser worker connected.

3. **Prove the box is fresh:**

   ```bash
   cat VERSION; git rev-parse --short HEAD
   ssh "$CONCIERGE_BOX" \
     'cat /opt/concierge/VERSION /opt/concierge/REVISION; systemctl is-active concierge-engine'
   VAULT=$(cd ansible && ansible-vault view host_vars/*/vault.yml)
   USR=$(printf '%s\n' "$VAULT" | sed -n 's/^vault_console_user: *"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p')
   PW=$(printf '%s\n' "$VAULT" | sed -n 's/^vault_console_password: "\(.*\)"$/\1/p')
   curl -s -u "$USR:$PW" "http://${CONCIERGE_BOX#*@}/api/v1/status"
   ```

   The console needs its login (HTTP Basic, user and password in the vault); only `/healthz` is
   open.

   `VERSION` and `REVISION` must equal local `VERSION` and HEAD, the service `active`, the status's
   `version` the same, `browser.connected` true.

4. **Report** the deployed version and commit, the play recap's `changed=` count, and the status
   line.

## When it fails

| Symptom | Do |
|---|---|
| `on '<branch>', not main` / `linked worktree` / `uncommitted changes` | Step 1 — merge or commit properly. Never bypass. |
| `make check` red | Fix the cause; a red bar is never deployed. |
| Vault password missing (`~/.vault/concierge`) | Stop and ask the user; never recreate it — the existing vault would be unreadable. |
| Box unreachable | Its address is a DHCP lease: find its MAC (in `CLAUDE.local.md`) on the LAN, then update `ansible/inventory.yml` and `CONCIERGE_BOX`. |
| Status never reports connected | `ssh … 'journalctl -u concierge-engine -n 50 --no-pager'` and debug the cause. |

Secrets change through the vault: `ansible-vault edit ansible/host_vars/<host>/vault.yml`, then
`make deploy`. Details: `ansible/README.md` (the private `ansible/` repo).
