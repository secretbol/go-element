# go-element

[![test](https://github.com/secretbol/go-element/actions/workflows/test.yaml/badge.svg)](https://github.com/secretbol/go-element/actions/workflows/test.yaml)
[![codeql](https://github.com/secretbol/go-element/actions/workflows/codeql.yml/badge.svg)](https://github.com/secretbol/go-element/actions/workflows/codeql.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

An [MCP](https://modelcontextprotocol.io) server that lets an AI agent read and send **end-to-end encrypted** Matrix / Element messages as you. It is one static Go binary built on [mautrix-go](https://github.com/mautrix/go) and the official [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk). It needs no `libolm` and no Python.

- Reads and decrypts history, including rooms from before this device existed (key-backup restore).
- Sends Markdown, as plain text and HTML, into encrypted rooms.
- **Writes are dry runs by default.** `send` only sends when the agent passes `yes: true`.
- Finds rooms by id, alias, user (the DM with them) or name. An ambiguous name lists the candidates instead of guessing.

## Quick start

```sh
go install -tags goolm github.com/secretbol/go-element/cmd/go-element@latest

# 1. log in: creates a NEW device called "go-element" (password from a prompt, a pipe or GO_ELEMENT_PASSWORD)
go-element login --homeserver https://matrix.example.org --user alice

# 2. restore old room keys from your server-side key backup (optional, once)
gopass show -o path/to/recovery-key | go-element import-keys

# 3. prove it works
go-element whoami

# 3b. verify the new device so other clients stop showing it as unverified (recovery key again)
gopass show -o path/to/recovery-key | go-element verify
go-element verify --status   # report only

# 4. give it to Claude Code (or any MCP client)
claude mcp add element -- go-element mcp
```

State lives in `~/.local/state/go-element` (`--dir` or `GO_ELEMENT_DIR`): `session.json` (access token, mode 0600) and `crypto.db` (device keys). **Never copy that directory to another machine**: two copies of one device break each other's encryption sessions. Log in again there instead.

## Tools

| Tool | Arguments | What it does |
| --- | --- | --- |
| `whoami` | | account and device the server acts as |
| `rooms` | `unread?`, `query?` | joined rooms with member and unread counts, unread first |
| `members` | `room` | joined members of a room |
| `history` | `room`, `limit?` (default 20, max 200) | newest messages, oldest first, decrypted when this device has the keys |
| `send` | `room`, `text`, `yes?` | Markdown message. **Dry run unless `yes: true`**; the dry run returns the resolved room and member count |
| `mark_read` | `room` | mark a room read up to its latest event |
| `verification` | | this device's verification state, and the emojis while `comparing` |
| `verification_confirm` | `match` | answer a pending emoji comparison; `true` only after you compared the emojis |

`room` is a room id (`!abc:server`), an alias (`#team:server`), a user (`alice` or `@alice:server`, meaning the two-member DM with them) or a room name (exact match first, then a unique substring).

## How it works

```
MCP client ──stdio──▶ go-element mcp ──▶ mautrix client ──HTTPS──▶ homeserver
                          │                  │
                     tool handlers      crypto helper (Olm/Megolm, pure-Go goolm)
                                              │
                                        crypto.db (SQLite)
```

- `internal/infra/matrix` wraps the Matrix client: session, rooms, history, send and key-backup restore.
- `internal/infra/mcpserver` registers the MCP tools. It holds no Matrix logic.
- `internal/cmd` is the CLI (`login`, `import-keys`, `verify`, `listen`, `whoami`, `mcp`).
- `mcp` logs to **stderr** only, because stdout carries the MCP protocol.

## Encryption notes

- The `goolm` build tag selects pure-Go Olm, so no C library is needed. SQLite (the crypto store) still needs cgo.
- `import-keys` unlocks secret storage with your recovery key, takes the backup key out of it and imports every session from the latest server-side backup. Run it again later to pick up newer backups.
- `verify` imports your cross-signing keys from secret storage with the recovery key, then signs this device and your master key. Other clients then show the session as verified. `verify --status` only reports, and exits non-zero while the device is unverified.
- `listen` is the interactive way: it waits for a verification request from your own other session (Element: Settings > Sessions > go-element > Verify session), prints the emojis, and confirms when you type `y`.
- `mcp` answers those requests too, while it serves. It accepts a request from your own account only and starts emoji (SAS) verification. Because stdin carries MCP, the comparison goes through two tools: `verification` shows the state (`idle`, `requested`, `ready`, `comparing`, `done`, `failed`) and the emojis, and `verification_confirm` with `match: true` confirms. Confirm only after you compared the emojis yourself; an unanswered comparison fails after 5 minutes.
- `send` reads the room's state from the server before every send and **refuses to send plaintext into an encrypted room**. (An early build sent plaintext because its state cache had not seen the room's encryption event.)
- `[undecryptable: ...]` means this device does not have that room key: the message is newer than the last backup and its sender has not shared the key.
- The recovery key and the session file are as sensitive as your password. Do not put them in a repository.

## Development

```sh
just build   # go build -tags goolm
just test    # go test -tags goolm -race ./...
just lint    # golangci-lint run
```

Tests run against an in-process fake homeserver, so they need no network. CI runs lint, tests with the race detector, a build and CodeQL on every push and pull request.

## License

[Apache-2.0](LICENSE)
