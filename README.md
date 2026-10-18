# opencode-agent

Personal Telegram assistant backed by a persistent OpenCode server. One live agent session. Plain text goes to it. Written in Go as a single static binary.

Commands: `/ping` answers `pong` without touching the agent. `/new` drops the live session and starts a blank one. `/model` alone reports the session model and thinking effort. `/model list` shows the available Zen models with their variants. `/model model#variant` switches them until the session ends, for example `/model deepseek-v4.1-flash-free#high`. A provider prefix still works and must be Zen. Only OpenCode Zen models are allowed. Names are checked against the server catalog, so unknown models and variants are rejected in chat instead of stalling the session. `/remember <fact>` saves one line to `MEMORY.md`, and `/forget <text>` removes matching lines. A `<name>:` prefix picks the file: `user:`, `soul:`, `identity:`, or `memory:`. Unknown prefixes and bare text land in `MEMORY.md`. Anything else is sent to the agent and the reply comes back as chat messages.

Memory lives entirely in `agent_workspace_dir`: `MEMORY.md` holds durable facts, `USER.md` holds stable preferences, `SOUL.md` holds personality and tone, `IDENTITY.md` holds the agent name and vibe, and `memory/YYYY-MM-DD.md` holds the daily log with a marker on every `/new`. Fresh sessions inject all four files ahead of the first question. Over-cap files truncate with a marker instead of failing the session. Curated files are capped near 2200/1375 chars, and an over-budget `/remember` fails instead of growing silently. Background on the pattern is in `docs/research/persistent-memory.md`.

Only one Telegram user ID gets answers. Everyone else is ignored. The agent works inside one dedicated workspace folder.

## How it works

`main.go` runs the Telegram bot with long polling. `serve.go` starts a private `opencode serve` child on a loopback port with a generated password, waits until it answers, and restarts it when it dies. `agent.go` holds the one live OpenCode session and talks to that child over HTTP with basic auth. `config.go` reads `config.toml` once at startup and fails fast on bad values. Unknown keys are rejected so typos never silently become defaults.

## Setup on the Mac mini

Prereqs: Go 1.27+, the opencode CLI with a logged in provider (`opencode auth login`).

1. Talk to BotFather on Telegram and create a bot. Keep the token.
2. Find your Telegram user ID. Message `@userinfobot` and copy the number.
3. Copy `config.toml.example` to `config.toml` and fill in the values.
4. Run `go mod download && go build -o opencode-agent .`.
5. Run `./opencode-agent` (`-config` points elsewhere when needed). The bot starts its own `opencode serve` and needs no separate server step.

Keys: `telegram_bot_token`, `telegram_allowed_user_id`, `agent_workspace_dir`, `model` (required, Zen only, for example `deepseek-v4.1-flash-free`). Optional: `agent_timeout_ms` (default `300000`), `model_variant` (default thinking effort, for example `high`).

Checks: `gofmt -l *.go` must print nothing, `go vet .` must pass, `go test .` runs the unit pin. `go test -run 'TestSupervisor|TestAgentLive' .` boots a supervisor-managed server per test and replays the agent loop against it. Only `TestAgentLiveModel` needs one env var: `OPENCODE_TEST_MODEL` in provider/model#variant form.

## Staying alive with launchd

One service is in `launchd/`. Edit the `YOU` placeholders first. Build the binary into the checkout, then run:

```
cp launchd/com.opencode-agent.telegram.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.opencode-agent.telegram.plist
```

Logs land in `~/Library/Logs/`. If the bot exits it restarts after 10 seconds. The bot also restarts its internal `opencode serve` whenever that child dies, so no second service watches the server.

Note: a bot restart loses the in memory session id. The next message opens a fresh session automatically.
