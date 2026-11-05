# Voice — a two-way check call with an OpenAI voice model

Status: built, 2026-10-28 (plan `docs/superpowers/plans/2026-10-28-voice-two-way.md`). Verified live
against OpenAI on 2026-10-28: the format object is `{"type":"audio/pcmu"}` with no `rate` (with it,
OpenAI answers `unknown_parameter`); `gpt-4o-transcribe` transcribes, writing a spoken code as
"4-7-2"; the model's first audio arrives about 0.9 s after it is asked to speak.

## Why

Voice's setup check is one-way today. calld rings the owner and plays a prebuilt recording that
says "472", and the owner types the code into the dialog. That proves the box can dial and play
sound. It does not prove the box can *hear*.

The long-term reason for Voice is the box making calls for the owner, such as booking a restaurant
or an appointment. Every one of those calls needs two-way speech.

This project builds that two-way path now, and uses it for one thing: the Voice setup check. The
box calls the owner, a voice model asks the check question aloud, the owner answers aloud, and the
box verifies the answer. *(The owner's request: "use an OpenAI model that supports talking"; "for
now we use it for verify two-way connections".)*

Design for the check itself:
`docs/superpowers/specs/2026-10-28-connectors-write-and-verify-design.md`, Phase 1. This document
changes only how Voice is verified.

## Goals

- **A spoken check call.** In Voice's setup dialog, step 2 (Verify), the owner types their number,
  picks a **Language** (English by default) and presses **Call me**. Then:
  1. The box calls. When the owner answers, the voice model greets them in that language and asks
     the check question: "What is A plus B?" A and B come from the same random 10–49 sum as the
     other connectors' checks.
  2. The owner says the answer aloud.
  3. The model thanks them and the box hangs up.
  4. If the answer is wrong or unclear, the model asks once more. A second miss ends the call.
  5. The dialog then shows one of:
     - **✓ Voice is added**, with nothing to type;
     - "Didn't hear the right answer", with **Call again**;
     - Busy or No answer, with **Call again**.
- **The box decides, not the model.** OpenAI transcribes what the owner says, and calld checks the
  transcript with a fixed rule. The model talks and listens; it never declares a check passed. This
  is the project rule: "The LLM lives inside steps; it never chooses the route."
  - The spoken rule differs from the written one (`verify.Challenge.Answered`, first number) in two
    ways, because speech isn't text:
    - calld first turns number words in the supported languages into digits. Transcripts may say
      "сорок два", or turn "trinta e dois" into "30 e 2".
    - Then the **last** number in the answer must equal A + B, because people correct themselves
      ("forty… forty-two"). *(My call.)*
  - This is a small, tested, per-language number reader for 0–99 in calld, not a model.
- **Languages.** *(My call.)* English (default), Português, Русский, Español. The model speaks many
  more, so the list is a constant that is easy to extend, together with that language's number
  reader.
- **Two-way audio plumbing that later calls can reuse.** While the call is answered and routed,
  calld:
  - streams the call's audio in both directions between the EC25's USB sound card and an OpenAI
    Realtime session;
  - ends that session when the call ends.

  The check is the first user of this plumbing. Booking calls will reuse it with other
  instructions.
- **No key, no change.** Until the owner adds the OpenAI API key, Voice keeps today's one-way check:
  the prebuilt recording and the typed code. In that mode the dialog shows no Language choice,
  because the recording is English only.

## Non-goals

- Answering incoming calls with the model.
- Calls that do errands (bookings, appointments). Those are later projects that build on this one.
- Recording the call's audio on the box.
- Choosing the voice, the model, or the instructions from the console.

## Anti-goals

- **The model never decides that a check passed**, and never ends up talking to anyone the owner
  didn't type into the dialog.
- **The API key never reaches a log, a prompt, the engine, or the browser**, and no daemon but
  calld can read it.
- **A call never outlives its limit**, and never stays connected with nobody speaking. A dropped
  OpenAI session hangs up the call.

## Constraints

- **Hardware.**
  - The EC25's sound card (`hw:EC25EUX,0`) plays and records 8 kHz, 16-bit, mono, little-endian.
    Both directions were checked on the box on 2026-10-28.
  - The call's voice reaches the card only while it is routed with `AT+QPCMV=1,2`, as today.
  - Reading the capture side, for example with `arecord` or an ALSA binding, is the builder's
    choice. It must not block playback.
- **OpenAI Realtime, over a WebSocket from calld.**
  - Endpoint: `wss://api.openai.com/v1/realtime?model=…`, with `Authorization: Bearer <key>`.
  - Model: `gpt-realtime-2.1`. *(My call: the full model, so the plumbing is ready for harder calls
    later. `gpt-realtime-2.1-mini` is the cheaper fallback.)*
  - Audio: G.711 μ-law at 8 kHz in both directions (`{"type":"audio/pcmu"}`), which matches a
    phone line with no resampling. S16 ↔ μ-law is a fixed table.
  - Input transcription and server turn detection must be on. The transcriber is told the call's
    language (`transcription.language`, accepted live on 2026-10-28).
  - The docs and the OpenAI forum disagreed on whether the μ-law format object carries
    `"rate":8000`. The live test settled it: it doesn't, and OpenAI refuses it
    (`unknown_parameter`).
    - If the live call shows the model won't take or return 8 kHz μ-law, the fallback is PCM16 at
      24 kHz with resampling both ways. That is a follow-up, not part of this build. *(My call.)*
  - **The session opens before the dial.**
    - If OpenAI refuses the key (invalid, revoked, no quota, or no access to the model), the phone
      never rings.
    - The check answers 502 with "OpenAI refused the key", and the dialog shows exactly that.
    - calld does not fall back to the one-way check, because that would hide the broken key.
      *(My call.)*
    - Two-way mode is on when calld has a key: `GET /calls/check` reports two-way `ready` or
      `none`.
- **The check itself.**
  - The challenge (A, B), its state, and the passed flag stay in calld's memory, as they do now. The
    engine's confirm reads `GET /calls/check` unchanged.
  - The call is capped at 60 s from answer. *(My call.)*
  - One call at a time, as today: a second request answers 409.
- **What is kept.**
  - The call record is stored as now.
  - What the box heard on the call is stored with that call record as "heard on the call", not as
    "the owner's answer". *(My call.)*
    - The label matters because of voicemail. If voicemail answers, ModemManager reports the call
      as active, the model talks to the greeting, and the call ends as "Didn't hear the right
      answer".
    - That is accepted. There is no voicemail detection.
  - Nothing else from the session is kept: no audio, and no model output beyond what is spoken.
- **Logs.** Logs carry no number, no transcript and no key. Errors are logged by type or OpenAI
  error code only, as in Phase 0.
- **The key.**
  - The owner has it: `OPENAI_API_KEY` in the repo's git-ignored `.env`.
  - The build copies it into the Ansible Vault (`ansible/host_vars/<host>/vault.yml`) without ever
    printing it.
  - **The key is checked once, before it is used** *(the owner's ask: "only one" check)*. Before it
    goes into the vault, the build runs a read-only
    `GET https://api.openai.com/v1/models/gpt-realtime-2.1`, which must answer 200. Checked on
    2026-10-28: 200, also for `gpt-realtime-2.1-mini`. calld makes no key check of its own.
  - It is delivered to calld only, in a file readable by `concierge-calls` alone.
  - calld still treats a missing key as "one-way mode", so a box without it keeps working.
- **Live first** *(the owner's answer: the key is available during the build)*:
  - The build starts with a live test against OpenAI from the Mac, with no phone. It settles the
    audio format before the session code is built on it.
  - The first live call to the owner's phone comes as soon as calld can carry the audio both ways,
    before the UI is built. That call settles the delay. The owner is present, and gives their
    number in chat at that moment.
- **Network.**
  - calld's unit today allows only Unix sockets (`RestrictAddressFamilies=AF_UNIX`).
  - Two-way voice needs outbound internet, so the unit gains `AF_INET AF_INET6`.
  - This is the only widening. calld still holds only its polkit voice right, `dialout` and
    `audio`, and still serves only its Unix socket.
  - hostd remains the only process that changes the network.
- **Data during a call.**
  - The Realtime session uses whichever uplink the box has.
  - On Wi-Fi this is independent of the modem. On the LTE fallback, the EC25 must carry the voice
    call and data at once, which depends on the network (VoLTE vs. fallback to 3G).
  - If the session can't be reached, the call ends as "Call failed". It never plays dead air.

## What the owner sees

- **Voice setup, step 2, with a key:**
  - fields: "Your phone number", **Language** (English, Português, Русский, Español) and
    **Call me**;
  - then "Calling…", then the outcome as above.
- **Voice setup, step 2, without a key:** exactly today's step (number, Call me, then "The code you
  heard").
- **The Voice page's call list:** the check call appears like any outgoing call. If the box stored
  the owner's answer, it shows the transcript under that call.

## Left to the builder

- **The API.**
  - The shape of the Voice check request: for example `POST /calls/check {number, language}`
    answering `{outcome}`, with a new outcome `"wrong"` for a second miss.
  - How the UI learns whether two-way mode is on, for example a field in `GET /calls/check`.
- **Audio and session code.**
  - The ALSA capture method and the buffering between card and socket.
  - The WebSocket library (an established Go one).
  - The model's instructions text and the voice.
- **Testing.** A fake Realtime server on loopback and a fake audio device for the tests. The live
  path is verified on the box, like `meow.go` and `gotd.go`.
- **Storage.** The archive schema change for the transcript.

## Calls I made (the owner hasn't ruled on these)

- **The language list:** English, Português, Русский, Español.
- **The model:** `gpt-realtime-2.1`.
- **One retry on a wrong answer, then the call ends.**
- **The call is capped at 60 s.**
- **What the box heard is stored with the call record, labelled "heard on the call". The audio
  isn't.** Voicemail answering ends the call as a wrong answer; there is no detection.
- **Without a key, one-way mode stays**, with no Language choice. A key OpenAI refuses is an
  error ("OpenAI refused the key"), not a fallback.
- **Spoken numbers:** number words in the four languages become digits, then the last number
  counts.
- **The session opens before the dial**, so a broken key never rings the owner's phone.
- **Audio is μ-law at 8 kHz.** PCM16 at 24 kHz with resampling is a follow-up, only if the live
  call needs it.
- **calld's unit gains outbound internet** (`AF_INET`/`AF_INET6`).
