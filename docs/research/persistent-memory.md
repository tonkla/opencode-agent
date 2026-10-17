# Adding persistent memory to the agent

This note compares the OpenClaw and Hermes memory patterns and recommends one for this bot. It is explanation, not a build plan. All claims come from the primary sources linked at the bottom.

## Start from files, not a service

Both systems store memory as plain Markdown files in the agent workspace. OpenClaw states the rule as no hidden state: the model remembers what is written to files, and every memory file stays readable in a text editor. Hermes keeps `MEMORY.md` and `USER.md` in `~/.hermes/memories/` and lets the agent edit them with a memory tool. For this bot, the matching home is `agent_workspace_dir`. `main.go` already creates that directory and probes that it is writable at startup.

## Keep two tiers: a small curated core and large episodic notes

OpenClaw splits memory into tiers. `MEMORY.md` and `USER.md` form the curated core and load at session start. `memory/YYYY-MM-DD.md` daily notes form the episodic tier: they hold raw observations, stay out of the startup prompt, and serve reads through search. Hermes prices the same split. About 1300 tokens of curated memory ride every prompt, and everything older lives behind session search over a SQLite store with FTS5 full-text search.

Copy the split. `MEMORY.md` holds durable non-profile facts and decisions. `USER.md` holds stable preferences as direct statements. Daily notes hold the running log. Nothing moves from episodic to curated without a deliberate consolidation step.

## Budget the curated core and enforce the budget

Hermes caps `MEMORY.md` at 2200 chars and `USER.md` at 1375 chars. A write that would exceed the cap fails, and the agent consolidates entries in the same turn before retrying. OpenClaw keeps the file on disk intact past the bootstrap budget but truncates the copy injected into context, which it treats as a signal to distill the file down. Either behavior works. Silent unbounded growth does not. Pick numeric caps near the Hermes values so injection stays near 1300 tokens.

## Move curation off the reply path

OpenClaw cites LongMemEval for the claim that what gets written matters more than how it is indexed, and it therefore consolidates in a scheduled background dreaming sweep instead of during replies. The sweep ranks candidates with fixed rules, merges winners into the curated files with one bounded model turn, and appends a summary of each rewrite to `DREAMS.md` with the prior copy kept. Hermes runs a background review after each turn that proposes saves, and an approval mode can stage those saves for user review.

This bot needs a smaller version of the same shape. On `/new`, ask the agent for a one-paragraph summary first and append it to the daily note before dropping the session. That mirrors the pre-compaction flush. Add a `/remember` command that appends one line to the right file. Keep an approval gate in mind for later, since unprompted background writes are the saves most likely to record a wrong assumption.

## Treat tool output as untrusted

OpenClaw tags every memory entry with an origin class of `owner`, `agent`, `untrusted`, or `system`, and content from the network taints the rest of the turn. Candidates with `untrusted` or `system` origin can never promote into the curated core, no matter how often they recur. Hermes scans entries for injection patterns before accepting them, since entries land in the system prompt. This bot serves one Telegram user id, so the trust story stays small: treat chat input as the owner, treat tool output as untrusted, and never promote tool output into `MEMORY.md` or `USER.md` without the owner asking.

## Never reingest recalled content

OpenClaw marks content injected from memory so that the pipeline never re-extracts it as a new memory. A fact recalled one hundred times stays one fact. Any search or injection step added here needs the same guard, or the daily notes fill with copies of the curated files.

## Refresh curated memory during long sessions

Hermes injects memory as a frozen snapshot at session start, so mid-session writes appear only in the next session. That staleness is acceptable for short CLI sessions. This bot holds one long-lived session until `/new`, so prefer a refresh each turn or reinject on demand after writes. A frozen snapshot would go stale within the first day.

## Recommendation for this repo

Add `MEMORY.md`, `USER.md`, and `memory/YYYY-MM-DD.md` under `agent_workspace_dir`. Inject the two curated files when `Agent.create` builds a session and after `Agent.Reset`. Flush a summary to the daily note before `Reset` drops the session. Hold the curated files near the Hermes character caps. Defer a SQLite session mirror until plain file search stops finding old work.

## Sources

- [OpenClaw memory architecture](https://docs.openclaw.ai/concepts/memory-architecture): tier model, provenance, dreaming gates, recall lanes.
- [OpenClaw memory overview](https://docs.openclaw.ai/concepts/memory): file layout, what goes where, budget truncation signal.
- [Hermes persistent memory](https://raw.githubusercontent.com/nimblecoorg/hermes-agent-mt/HEAD/website/docs/user-guide/features/memory.md): char limits, frozen snapshot format, memory tool actions, session search, approval gate, background review.
- Current repo state: [agent.go](/Users/ss/work/opencode-agent/agent.go) session lifecycle, [main.go](/Users/ss/work/opencode-agent/main.go) workspace setup, [README.md](/Users/ss/work/opencode-agent/README.md) single-session behavior.
