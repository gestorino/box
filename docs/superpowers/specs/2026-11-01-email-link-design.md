# Confirming a registration by an email link

> Superseded in transport by 2026-11-03-listeners-design.md: the engine now waits on a listener; /email/received and /email/link are gone.

Date: 2026-11-01. Status: built. It closes the "not supported yet" left by
`2026-10-28-browser-sign-ins-design.md` ("Email link confirmation").

idealista.pt confirms a new account only by a link in an email. The owner asked for it to work like
the SMS reader: the box reads the message itself and puts what it found into the run. The one real
difference from a code is that an email holds many links, so the description says which.

| | SMS | Email link |
|---|---|---|
| The description says | sender, code length | sender, how the link starts |
| The box reads | the newest new SMS from the sender | the newest email from the sender to this attempt's address |
| The box takes | the code of that length | the first `href` that starts so |
| The run | types the code | opens the link in its tab |
| Nothing in 5 min | the run fails | the run fails |

```yaml
link: {sender: naoresponder@idealista.pt, starts_with: https://www.idealista.pt/validar-email}
```

- **Where:** a step with `link` has no `path`, `fills` or `code`. `verify: email_link` and a link step
  go together, and `starts_with` must be on an allowed origin.
- **`emaild`:** `GET /email/link?to&sender&starts_with` answers that link and nothing else of the
  email. The link must end where `starts_with` ends or go on with `/`, `?` or `#`, so
  `…/validar-email.evil` doesn't count.
- **Engine:** `codeAsk` answers the worker's `email_link` request, polling on the code wait's clock. A
  link that never arrives fails the registration and wipes the profile, however signed in the site
  looks: that account isn't confirmed.
- **Worker:** the step asks, opens the link once through `navigate`, and records nothing of it. The
  profile's allowed domains fence it, redirects included. It is never the model's and never retried,
  because a confirmation link may work only once.

## Accepted

- A link that opens but doesn't confirm (expired, say) can still end signed in, if the site shows its
  signed-in header before confirmation. It arrives fresh seconds earlier, so this is unlikely.
- The worker's network trace and the run's recording hold the URL, already used by then. browser-use
  may log a URL to the worker's stderr if the page's readiness times out.

## Not built

`email_code`, a link in an SMS, a link's text-only email (only an HTML `href` counts).
