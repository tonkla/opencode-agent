# OpenCode v2 live response mechanism

`sendDraft` looked dead because progress had no live source: this bot polled
`GET /api/session/:id/message`, and that list only gains the finished reply.
Live text is fixable — it rides a separate SSE bus. All claims below come
from the primary sources linked at the bottom, verified against opencode
v2.0.32 (the version this bot manages).

## The message list is completion-only

`GET /api/session/:id/message` returns stored messages. An assistant message
gains its `completed` timestamp when the turn settles, and the `idle` marker
ends the turn. Polling it can report at most one final value, never a
growing partial. Any preview fed from this endpoint shows nothing until the
answer is already done.

## Live text arrives on GET /api/event

`GET /api/event` (`event.subscribe` in the
[served OpenAPI spec](http://127.0.0.1:18799/openapi.json)) is a
`text/event-stream` endpoint. Frames are `data: {json}\n\n`, the first frame
is `server.connected`, and `: heartbeat` comments arrive every 15 seconds.
On v2.0.32 the endpoint takes no query parameters: the stream is
instance-wide and a slow consumer overflows and fails it, so each turn
should open its own subscription and filter client-side.

## The delta events to consume

- `session.text.delta` with
  `data: {sessionID, assistantMessageID, ordinal, delta}` carries one text
  fragment. It is ephemeral (live-only, never replayed).
- `session.text.ended` carries the full `text` and is the replayable
  boundary for the same part.
- Sibling fragments exist for reasoning (`session.reasoning.delta`) and
  tool input (`session.tool.input.delta`); the draft preview only needs
  text.

Filter by `data.sessionID`: one server hosts every Telegram user's session,
and every subscriber sees every session's events.

## What this bot does now

`Agent.AskWithProgress` opens the SSE subscription for its own session ID
while the existing message poll still decides completion. Deltas accumulate
in arrival order and each accumulation fires `onProgress`, which is what
`sendDraft` forwards to Telegram. The poll-based `latestPartial` path stays
as a fallback. A broken stream only loses the ephemeral preview; the final
answer still arrives through the poll.

## Sources

- Served spec of the managed server: `GET /api/event` →
  `text/event-stream`, `operationId: event.subscribe`
  (opencode v2.0.32, probed live on 127.0.0.1)
- `packages/server/src/handlers/event.ts` (`EventHandler`, SSE framing,
  heartbeat) and `packages/server/src/event-feed.ts` (`data: {json}`
  framing, volatile/overflow contract) — sst/opencode, `dev` branch
- `packages/protocol/src/groups/event.ts` (`event.subscribe`, `/api/event`)
  and `packages/schema/src/session-event.ts` (`session.text.delta` /
  `session.text.ended` shapes, ephemeral vs durable) — sst/opencode,
  `dev` branch
- `packages/schema/src/event.ts` (envelope `{id, type, created, data}`) —
  sst/opencode, `dev` branch
