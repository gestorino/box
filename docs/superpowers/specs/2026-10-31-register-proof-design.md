# A registration proven by the site's email

> Superseded in transport by 2026-11-03-listeners-design.md: the engine now waits on a listener; /email/received and /email/link are gone.

**Why.** A flow that finished, or even a profile that is signed in, is the box's own reading of a
page. Booksy says it itself: its welcome email reaches the plus-address about 40 s after the SMS code
is accepted. Where a site sends such an email, that email is what makes the registration true.

## Design

- **The description says what proves it.** Optional `register_proof` in `account.yaml`:
  `{channel: email, sender: no-reply@booksy.com, subject: Bem-vindo(a) ao Booksy}`. It needs a
  `box_email` requirement, since the email has to reach the box.
- **`emaild` answers one question.** `GET /email/received?to=&sender=&subject=`: has an incoming email
  from exactly this sender, to exactly this address, with this text in its subject, been archived?
  It returns only `{"received": bool}`, no content.
- **The engine waits for it** when the flow finished or the profile is signed in, polling every 5 s
  for up to 5 minutes (the code wait's values). The run's log says so.
- **Only the email counts.** With `register_proof`: the email marks the account registered. Without
  it, the run is a warn, "the confirmation email never arrived", even when signed in, and the account
  is not registered, so Try again takes the next plus-address. A site without `register_proof` keeps
  today's rule.

A plus-address is new for every attempt, so an older email can never prove a new attempt.

## Not doing

- Accepting an email that arrives after the wait: that attempt stays unregistered, and the account it
  made on the site goes unused.
- Reading the email's content, or clicking anything in it.
