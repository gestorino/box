# Voice: keep a call's transcript or audio, for debugging

Status: draft, 2026-10-28.

## Why

The box now holds real phone calls itself: today the Voice check call, soon calls it places for an
errand, such as ringing a salon. When one of those goes wrong, nothing on the box shows what was said
or how it sounded. The owner found the check's goodbye spoken in English on a Russian call, and only
by listening on the phone.

The owner wants two debugging aids, each a setting: **keep the transcript** and **keep the audio**.
Once calls work, the box should keep only **the result, maybe with a brief summary of the
conversation** (the owner's words). The owner will later limit both settings to specific numbers.

**Success:**
- With "keep the transcript" on, the Voice page shows a call's full conversation, turn by turn:
  what the box said and what the other side said.
- With "keep the audio" on, the call can be played back on the Voice page.
- With both off, which is the default, each call keeps its log entry, its result and a short summary,
  and nothing else.

## Scope: which calls have anything to keep

Only calls **the box itself is on**, the ones `calld` holds through a voice model (`Caller.Converse`).
Today that is the Voice check; later, errand calls. The box never answers calls to the SIM: the Voice
design lists answering incoming calls with the model as a non-goal. So other people's calls have only
their log entry, as today.

## Goals

### Always kept, for every call the box holds

- **The call log entry,** as today: number, direction, started, answered, ended, outcome. It is under
  the existing "Keep the call log for" retention.
- **The result:** the outcome the call's script reports (`passed`, `wrong`, `failed`; errand scripts
  will add theirs). For a call that was never answered, the log's outcome (`busy`, `no-answer`,
  `failed`) is the result.
- **A brief summary:** one to three plain lines, for example: *Asked 23 + 19 in Russian; heard "сорок
  два"; passed.* The summary replaces today's `heard` field, which goes away; the old data is migrated
  into the summary column. *My call:* each script writes its own summary from what it knows, so the
  check's summary is deterministic and costs no model call. When errand calls arrive, their scripts
  may ask a model for the summary. A `Script` returns the summary alongside the outcome. A call that
  was never answered gets no summary, because its outcome says it all.
  - `heard` is removed everywhere: the API, `src/ui/src/lib/calls.ts` and the Voice page move to
    `summary` in the same change (no backward compatibility, per CLAUDE.md).

### Setting: keep the transcript (off by default)

- Turned on, every call the box holds keeps its **conversation, turn by turn**: speaker (`box` or
  `them`), the text, and the time.
  - The box's turns are **what the session actually spoke**: the voice session's own transcript of its
    output, not the instruction it was given. For the check too. The point is debugging, and the bug
    that started this, a Russian call's goodbye spoken in English, would be invisible in a transcript
    of the instructions. *My call.* The `Talker` gains a channel for the box's spoken text, which
    `realtime.Session` fills from its output-transcript events.
  - Their turns are the transcriber's text, as `Heard` gives today.
  - **The order** comes from `Converse` wrapping the `Talker`: every turn is recorded with its time as
    it happens, so scripts don't change shape for it. A retried question and the answer it replaced
    are both turns. *My call.*
- The Voice page shows it under the call, as a short chat.
- Turned off, nothing is written. Turning it off does not delete what was kept: retention does that.

### Setting: keep the audio (off by default)

- Turned on, every call the box holds keeps its **sound, both directions**: one file per call, two
  channels, with *them* (the sound card's input) and *the box* (what it played out).
- The Voice page shows a player on the call, plus a download link.
- The file stays on the box under `calld`'s own user. It is reachable only through the console login,
  and is never sent anywhere else.
- **Alignment.** *My call:* a clock-driven 20 ms recorder paced by the sound card's input. Each 20 ms
  frame writes *them* (what came in) and *the box* (what was played out in that frame). It pads
  silence on whichever side said nothing, so the channels stay aligned in time and a playback sounds
  like the call did.
- **Serving.** Whole files (a call is a few MB at 8 kHz stereo); no range requests. The engine
  forwards the bytes with `Content-Type: audio/wav`, and the download link adds
  `Content-Disposition: attachment`.

### Debug retention

- Transcripts and audio share **their own retention**, separate from the call log's: "Keep transcripts
  and audio for", with choices of 7, 30 or 90 days. Default: 30. *My call:* debug material shouldn't
  pile up for a year like the log.
- Older transcripts and audio are deleted; the log entry, result and summary stay until the call log's
  own retention removes them.
  - The age counts from the call's start.
  - Purging runs where the call log's purge already runs.
  - An audio file with no call row (a crash between the two writes) is swept too.
  - In the UI, a call whose transcript or audio has aged out just shows neither. *My calls.*

### The warning (the owner's decision: a warning in Settings only, no announcement on the call)

Turning either setting on asks for confirmation with this text:

> Recording or transcribing another person's words without their consent is a crime in Portugal
> (Código Penal, art. 199). Turn this on only for calls with people who agreed, such as your own
> test calls.

The same sentence stays visible under both settings while either is on. The box says nothing about
it during the call. The confirmation is the UI's. hostd stores the switches as given and doesn't
require an acknowledgement. *My call.*

### Where the settings live

- **hostd** owns the box's settings: the two switches and the debug retention go into `settings.db`
  beside the existing retentions, under `PUT /settings/...` as today.
- `calld` reads them through the read-only settings socket and `internal/hostsettings`, as it already
  reads its retention. It reads them **when a call starts**, so a change never splits a call
  half-kept.
- Until hostd first answers, `calld` keeps nothing extra. It never keeps on a guess.
  - `hostsettings` tracks "never answered" apart from "answered: off".
  - After a first answer, the last value seen holds while hostd is away. A switch the owner turned on
    stays on, and one turned off stays off. *My call.*

## Non-goals

- Announcing the recording or transcription on the call (the owner's decision, for now).
- Per-number settings. They come next (the owner's plan). The settings are stored so a list of numbers
  can be added beside each switch without changing what the switches mean.
- Anything for calls the box doesn't hold, such as calls to the SIM from others.
- Sending audio or transcripts anywhere: no upload, no sharing. A model call for a summary, later, is
  the only exception, and it would see only the transcript text.

## Anti-goals

- Audio or a transcript written while its setting was off.
- A file readable by anyone but `calld`'s user (mode 0600 inside a 0700 directory).
- The audio or transcript ever appearing in a log line.
- Debug material outliving its retention because hostd was unreachable. Purging uses the last value
  `calld` saw, as the existing retention does.

## Constraints

- `make check` is the merge bar. Tests never touch the network: fakes for the modem, the sound card
  and the voice session. `calld`'s logic is tested with its existing fakes (`fakeTalker`, the fake
  `Duplex`).
- `calld` owns `calls.db` and its files; hostd owns settings. The engine only forwards
  `/api/v1/calls/*` reads, plus the new audio file.
- `src/ui/src/lib/calls.ts` mirrors the Go types; the UI follows `docs/STYLE-GUIDE.md`.

## Left to the builder

- The WAV writer: standard library only, a 44-byte header, then interleaved S16_LE at 8 kHz.
- The table shape for turns, and the migration of `heard` into the summary.
- The exact routes, for example `GET /calls/{id}/turns` and `GET /calls/{id}/audio`, and how the engine
  forwards the audio (as bytes with `Content-Type: audio/wav`).
- How the box's own turns are captured for model-led calls, from the session's output-transcript
  events. Today only the check exists, and its sentences are known.
