# Remote access, part 3: the box (relayd)

Status: draft for the owner's review, 2026-11-08.

Part of three. **Only this one is public** (the owner): the control plane (a Cloudflare Worker:
accounts, the box registry, claiming, frps's hooks) and the relay (frps on a VPS) are the maker's
service, built and kept in a private repo of their own (`cloud/`, git-ignored here, like
`ansible/`). What the box needs from them is the contract below; nothing in this tree depends on
their code.

What stays checkable from this tree alone: the phone's TLS ends on the box, the box checks the
relay's certificate, and the box tells the control plane only its `box_id`, its secret, the
owner's email (to claim) and its version — never what it does.

## The contract with the service

- **The control plane** (`https://gestorino.com`, JSON):
  - `POST /box/v1/claim/start {email}` → `200 {}` whether or not the email has an account (a code
    is mailed when it does; 6 digits, 15 minutes, 5 tries).
  - `POST /box/v1/claim/finish {email, code, box_id, secret}` → `200 {name, box_domain, relay}`
    (`relay` is `relay.gestorino.net:7000`); `400 {error: "wrong code" | "expired code" | "no code"}`;
    `409 {error: "box already claimed"}`. `secret` is 256 random bits, base64url.
  - `GET /box/v1/status` with headers `X-Box-Id`, `X-Box-Secret`, `X-Box-Version` →
    `200 {claimed: true, name}`, or `401` once the box was removed.
- **The relay** (`relay.gestorino.net:7000`, frp v0.71.1): frpc over TLS with a certificate a
  public CA signed for `relay.gestorino.net`; `metadatas` `box_id` and `secret`; one `https` proxy
  named after the box's `box_id` (frp's proxy names are global on the relay, so the relay refuses a
  proxy under any other name) and the relay sets its `subdomain` to the box's name; phones arrive on 443 by SNI
  `<name>.gestorino.net`, passed through undecrypted. A box the control plane doesn't know is refused
  at `Login`. Removal reaches the box only by its status call.
- **Names** are 10 characters of `a-z2-7`, made by the control plane, never reused.

## Why

The box must claim itself to its owner's account, hold a connection to the relay, end the phones'
TLS itself with a certificate only it holds, and let in only the phones it issued a certificate
to. A **service of its own** does it (*my call*, the owner asked): like every daemon on the box,
under its own user, with no root and no reach into the other services' data.

**Success:** on the console at home the owner types their email, then the code from the mail; the
console says "Connected as `<name>.gestorino.net`"; they issue a certificate for their phone,
install it, and from mobile data the phone opens `https://<name>.gestorino.net` and gets the
console. Another phone, without a certificate, can't complete the handshake. After the box is
removed in the account, the console says it is not connected within 10 minutes.

## Goals

### The service

- **`cmd/relayd` + `internal/relay`** (Go), own user `concierge-relay`, `StateDirectory`
  `/var/lib/concierge-relay` (0700), one `relay.db` (the pure-Go SQLite the daemons use).
- **A socket of its own** (`/run/concierge-relay/relayd.sock`, HTTP/2 only, `internal/h2c`, group
  `concierge-relay-clients`, of which the engine's user is a member). The engine forwards
  `/api/v1/relay/*` to it (`api.SocketProxy`), behind the console login.
- **A secretd caller**, namespace `relay` (`-secrets-socket`, required): `box-secret` and
  `device-ca` (the CA key and its certificate, one value). secretd down: claiming and issuing answer 502 "secrets unavailable"; a claimed
  box whose secret secretd can't give doesn't connect, and says so.
- **Network:** it alone of the box's daemons talks to the control plane and the relay (outbound
  HTTPS and TCP 7000), and listens on `127.0.0.1:8443`, nothing else.
- Unit and user wherever the box layout lives when this is built (today `ansible/`; the installer
  of `2026-11-07-box-install-update-design.md` once it lands); the release carries `relayd`.

### Three libraries, one process

- **frp's client** as a Go package (`github.com/fatedier/frp/client`), not a separate frpc: it
  dials `relay.gestorino.net:7000` over TLS, checks the relay's certificate against the system's
  roots with `serverName = relay.gestorino.net` (without a trusted CA set, frpc doesn't verify the
  server, and the box secret would go to anyone in the middle; the builder confirms in frp's code),
  sends `box_id` and `secret` in `metadatas`, and registers one `https` proxy to `127.0.0.1:8443`
  (any subdomain: the control plane rewrites it to the box's name). It reconnects by itself.
- **certmagic** (Caddy's ACME library) for the box's certificate for `<name>.gestorino.net`, by
  **TLS-ALPN-01 only** (port 80 isn't routed to the box): Let's Encrypt's check reaches the box
  through the relay on 443. Its storage (account key, certificate, key) under the state directory
  (*my call*: certmagic's file storage, not secretd).
- **A TLS server** on `127.0.0.1:8443`: `GetConfigForClient` answers ACME's `acme-tls/1`
  handshake with certmagic's challenge (no client certificate asked), and every other handshake
  **requires a client certificate** issued by the box's device CA whose serial is in the active
  list (`VerifyConnection`). Then `httputil.ReverseProxy` to the engine at `127.0.0.1:80`
  (WebSockets pass: noVNC's live view). The console login (HTTP Basic) still applies: the
  certificate says which device, the login who (*my call*: the console's own login stays,
  `docs/specs/phase1-standalone-runner-spec-en.md` §4.3).

### Claiming

States: **unclaimed** → **claiming** (a code was mailed) → **claimed**.

1. First start: relayd makes `box_id` (a UUID, in `relay.db`) and `box-secret` (256 random bits,
   in secretd). Nothing goes on the network.
2. The console's Remote access tab, unclaimed: "Your account's email" → `POST /relay/claim/start`
   → the control plane's `claim/start`.
3. "The code from the mail" → `POST /relay/claim/finish` → the control plane's `claim/finish`
   with `box_id` and the secret → `{name, box_domain, relay}` stored → claimed.
4. Claimed: the frp client starts; certmagic gets the certificate; the tab shows
   "Connected as `<name>.gestorino.net`" (or what it is waiting on: the relay, the certificate).
5. Every 10 minutes `GET /box/v1/status` (sending the box's version). **`401`: removed** → stop the
   frp client, delete the certificate, the secret and the devices, make a new `box_id` and secret,
   back to unclaimed. **Any other failure** (no network, the control plane down): keep going as is.
   A `Login` the relay refuses is logged and retried, never taken as removal (only the status call
   decides it).

### Devices

- **The device CA** is relayd's own: an ECDSA P-256 key (`device-ca` in secretd, the key and its certificate as one value) and a
  self-signed CA certificate, made at relayd's first start. It signs only device certificates.
- **Issue** (Remote access tab, a label such as "Owner's iPhone"): a P-256 key and a certificate
  for one year, made on the box, given once as a `.p12` with a random password shown once (iOS
  refuses a `.p12` without one); the key is not kept. The builder picks the PKCS#12 encoder
  (`software.sslmate.com/src/go-pkcs12`) and checks the file installs on an iPhone (Safari →
  profile) and on Android ("VPN & app user certificate").
- **The active list** (`relay.db`: serial, label, issued, expires): only those serials are let in.
  **Revoke** removes one, at once. A device that expires is gone; issuing again is the owner's.
- Issuing is offered only from the LAN (*my call*), so a stolen device certificate can't mint
  more: relayd marks every request it forwards (a header it first strips from the incoming
  request), the engine trusts the mark only from loopback, refuses issuing on a marked request,
  and the tab's issue button is off there.

### The console

- **Settings gets a Remote access tab** (*my call*: next to Wi-Fi and SIM; `CLAUDE.md`'s "Settings
  is Wi-Fi, SIM and the AI key only" gains it): the claim steps, the state (connected as …, waiting
  on …, not connected and why), and the devices (issue, revoke). The style guide binds it: the five
  statuses as icon + word, honest empty states.
- `src/ui/src/lib/api.ts` gains the relay's types.

## Checked first, on a real iPhone

Whether Safari, with a device certificate installed as a profile, presents it on a **WebSocket**
handshake (the live view) and from a **Home Screen** icon. Unconfirmed either way; the app path is
confirmed by Home Assistant's iOS code. If Safari doesn't, the fallback is the owner's to pick (the
live view from outside waits for the app, or a token for the WebSocket alone, which lets a
handshake without a certificate through); nothing else in this design changes.

## Non-goals

- The app (it will hold its key in the Secure Enclave and present it itself), push, P2P.
- Fewer actions over the relay than on the LAN (beyond issuing devices).
- Taking the console off the LAN: it stays on port 80 as today.

## Testing

- **Hermetic, in `make check`:** a fake control plane (`httptest`) speaking the contract above,
  a stand-in relay (frps in-process on loopback from frp's own packages, with a test
  configuration that follows the contract: `src/engine/relaytest`, public and outside `internal/` so another module can import it, also used by the
  private repo to test its real configuration), a fake secretd, and **the
  server certificate injected** (no ACME in the merge bar; certmagic's issuing is proven live).
  It covers: the claim states, wrong and expired codes as the control plane answers them;
  `401` → unclaimed with a new identity; other status failures change nothing; a handshake without
  a certificate, with an unknown one and with a revoked one refused; an issued one through to a
  fake engine, a WebSocket included; the ALPN challenge handshake asks for no certificate;
  issuing refused over the relay.
- **Live, by hand:** once on the owner's box after deploy, against the real service: claim, a
  phone on mobile data, the live view of a run, removal.
