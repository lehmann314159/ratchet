# Haiku generator web app — rough spec

A small Go + HTMX web app that generates a haiku using a locally connected open
model (Ollama).

## Input

A form with:
- **Theme** (required) — e.g. "autumn", "loneliness", "the sea"
- **Keyword** (required) — a specific word the haiku must contain
- **Nature element** (optional) — e.g. "maple", "heron", "frost"

On submit, the app prompts the local model for a 5-7-5 haiku built around those
inputs and swaps the result into the page via HTMX.

## Generation

- The model is called over Ollama's HTTP API. Endpoint and model name come from
  `--ollama` and `--model` flags, with env-var fallback.
- The prompt asks for exactly three lines, 5-7-5 syllables, containing the
  keyword and (if given) evoking the nature element.
- Whatever three non-empty lines come back are displayed as-is. No syllable
  validation. If the model is unreachable, errors, or returns something that
  isn't three non-empty lines, swap in an error fragment and re-render the form
  with the user's inputs still filled in.

## Persistence (SQLite)

- Every generated haiku is stored with its theme, keyword, nature element (may be
  null), the three lines, and a timestamp.
- A `/history` page lists past haiku, newest first.
- Every email send is logged: which haiku, recipient name, recipient email,
  timestamp.

## Email

- From the haiku result, the user can email it to a recipient by entering a
  **recipient name** and a **recipient email address**.
- The sender is hardcoded as `lehmann314159@gmail.com`.
- "Sending" means writing the message as an RFC 5322 `.eml` file into a local
  outbox directory (configurable via flag). No real SMTP, no credentials.

## Out of scope

Authentication, editing/deleting saved haiku, multiple senders, real SMTP
delivery, rate limiting, styling beyond a minimal readable layout.
