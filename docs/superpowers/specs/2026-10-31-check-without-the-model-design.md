# The signed-in check reads the page itself

**Why.** A check only asks whether a page shows one text, which the description already names, and the
model charged about $0.04 to read it (run 28: 1 call, most of it a cache write that is never reused).

- **The page answers when it clearly can** (`replay.py`'s `look_signed_in`): signed-in text without the
  signed-out text is yes, the other way round is no, once that has held for 2 s. It runs through the
  same `run_step`, so the gate, containment and traces are the same.
- **Anything else goes to the model**, after 10 s: both texts, neither (a captcha, an error page, a
  redesign), or a site with no signed-out text that doesn't show the signed-in one.
- **Read from the elements a person can reach.** On Booksy these held steady from the first read,
  signed in and out, for 15 s. The raw DOM flickers with hidden elements; the selector map does not.
