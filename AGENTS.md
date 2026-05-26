# Crush Development Guide

## Project Overview

Crush is a terminal-based AI coding assistant built in Go by
[Charm](https://charm.land). It connects to LLMs and gives them tools to read,
write, and execute code. It supports multiple providers (Anthropic, OpenAI,
Gemini, Bedrock, Copilot, Hyper, MiniMax, Vercel, and more), integrates with
LSPs for code intelligence, and supports extensibility via MCP servers and
agent skills.

The module path is `github.com/charmbracelet/crush`.

## Architecture

### Top-Level Layout

```
main.go                            CLI entry point (cobra via internal/cmd)
internal/
  app/app.go                       Top-level wiring: DB, config, agents, LSP, MCP, events
  cmd/                             CLI commands (root, run, login, models, stats, sessions)
  config/
    config.go                      Config struct, context file paths, agent definitions
    load.go                        crush.json loading and validation (layered: global → workspace)
    store.go                       ConfigStore — owns Config + runtime state + persistence
    provider.go                    Provider configuration and model resolution
    resolve.go                     Variable resolution with shell expansion + 5m timeout
  agent/
    agent.go                       SessionAgent: runs LLM conversations per session
    coordinator.go                 Coordinator: manages named agents ("coder", "task")
    hooked_tool.go                 Decorator that runs PreToolUse hooks before tool execution
    prompts.go                     Loads Go-template system prompts
    templates/                     System prompt templates (coder.md.tpl, task.md.tpl, etc.)
    tools/                         All built-in tools (bash, edit, view, grep, glob, etc.)
      mcp/                         MCP client integration
    loop_detection.go              Detects agent loops by hashing recent tool-call signatures
    agentic_fetch_tool.go          Sub-agent tool that lets the agent fork a research agent
  hooks/                           Hook engine: runs user shell commands on hook events
    hooks.go                       Decision types, aggregation logic, event constants
    runner.go                      Parallel hook execution, timeout, dedup
    input.go                       Stdin payload builder, env vars, stdout parsing (Crush + Claude Code compat)
  session/session.go               Session CRUD backed by SQLite
  message/                         Message model and content types
  db/                              SQLite via sqlc, with migrations
    sql/                           Raw SQL queries (consumed by sqlc)
    migrations/                    Schema migrations
  lsp/                             LSP client manager, auto-discovery, on-demand startup
  ui/                              Bubble Tea v2 TUI (see internal/ui/AGENTS.md)
  permission/                      Tool permission checking and allow-lists
  skills/                          Skill file discovery and loading (SKILL.md standard)
  shell/                           Bash command execution with background job support
  server/                          HTTP server over Unix socket / Windows named pipe
  client/                          HTTP client for connecting to a remote Crush server
  backend/                         Transport-agnostic business logic layer over app.App
  proto/                           Wire protocol types (shared between server and client)
  pubsub/                          In-process pub/sub broker for cross-component messaging
  csync/                           Thread-safe concurrent data structures (Map, VersionedMap)
  filetracker/                     Tracks files touched per session
  history/                         Prompt history
  event/                           Telemetry (PostHog)
  projects/                        Project discovery (.git repos in home dir)
  oauth/                           OAuth token handling (Copilot, Hyper)
  swagger/                         Generated OpenAPI spec
  update/                          Self-update checking
  version/                         Build version injection
```

### Client/Server Architecture

Crush runs as a local HTTP server (Unix socket on Linux/macOS, Windows named
pipe on Windows) and optionally a CLI client that connects to it. The `server/`
and `client/` packages handle transport; the `backend/` package provides
transport-agnostic business logic.

- **Server** (`internal/server/server.go`): HTTP server bound to
  `/tmp/crush-{uid}.sock` (or `npipe:////./pipe/crush-{uid}.sock` on Windows).
  Uses `net/http` with swagger docs.
- **Client** (`internal/client/client.go`): HTTP client for remote Crush
  server access.
- **Backend** (`internal/backend/backend.go`): Manages multiple workspaces,
  delegates to `app.App` per workspace.
- **Proto** (`internal/proto/`): Wire types shared between server and client
  (workspace, session, agent, permission, LSP, MCP, skills, tools).

The server mode is experimental — most users run Crush in local mode where the
TUI and agent share a single process.

### Key Dependency Roles

- **`charm.land/fantasy`**: LLM provider abstraction layer. Handles protocol
  differences between Anthropic, OpenAI, Gemini, etc. Used in `internal/app`
  and `internal/agent`.
- **`charm.land/bubbletea/v2`**: TUI framework powering the interactive UI.
- **`charm.land/lipgloss/v2`**: Terminal styling.
- **`charm.land/glamour/v2`**: Markdown rendering in the terminal.
- **`charm.land/catwalk`**: Snapshot/golden-file testing for TUI components.
- **`charm.land/fang/v2`**: Fuzzy finding for completions.
- **`charm.land/x/vcr`**: Record/replay for LLM API calls in tests.
- **`charm.land/ultraviolet`**: Screen-based rendering for the TUI chat view.
- **`charm.land/x/powernap`**: LSP auto-discovery and configuration defaults.
- **`charm.land/log/v2`**: Charm's structured logging.
- **`sqlc`**: Generates Go code from SQL queries in `internal/db/sql/`.
- **`github.com/mvdan.cc/sh/v3`**: POSIX shell interpreter used for cross-platform
  command execution without requiring a real shell.
- **`github.com/sourcegraph/jsonrpc2`**: JSON-RPC 2.0 for LSP communication.
- **`github.com/spf13/cobra`**: CLI framework for subcommands.

### Key Patterns

- **Config is a Service (`config.ConfigStore`)**: Not global state. Owns the
  pure-data `Config`, working directory, variable resolver, known providers,
  and persistence. Config is loaded from multiple layers (global `~/.local/share/crush/crush.json`,
  workspace `.crush/crush.json`). Supports hot-reload via `AutoReload()`.
- **Tools are self-documenting**: each tool has a `.go` implementation and a
  `.md` or `.md.tpl` description file in `internal/agent/tools/`.
- **System prompts are Go templates**: `internal/agent/templates/*.md.tpl`
  with runtime data injected via the `prompt.Prompt` type.
- **Context files**: Crush reads AGENTS.md, CRUSH.md, CLAUDE.md, GEMINI.md
  (and `.local` variants) from the working directory for project-specific
  instructions. See `defaultContextPaths` in `internal/config/config.go`.
- **Persistence**: SQLite + sqlc. All queries live in `internal/db/sql/`,
  generated code in `internal/db/`. Migrations in `internal/db/migrations/`.
- **Pub/Sub (`internal/pubsub`)**: Decoupled communication between agent, UI,
  and services. Two delivery semantics:
  - `Publish` — non-blocking and lossy. Drops events if subscriber's channel
    is full (buffer: 4096). For high-frequency updates like streaming tokens.
  - `PublishMustDeliver` — bounded-blocking with 50ms per-subscriber timeout.
    For terminal events (finish, tool result, error, cancel).
- **Hooks**: User-defined shell commands in `crush.json` that fire before
  tool execution. The engine (`internal/hooks/`) is independent of fantasy
  and agent — it takes inputs, runs commands, returns decisions. The
  `hookedTool` decorator in `internal/agent/hooked_tool.go` wraps tools at
  the coordinator level. Hooks run before permission checks.
  - Hook output format supports Claude Code compatibility
  - Exit code 49 halts the whole turn
  - Hooks can rewrite tool input via `CRUSH_TOOL_INPUT_MODIFIED`
- **Thread-safe data structures (`internal/csync/`)**: The project uses custom
  concurrent maps (`Map`, `VersionedMap`) instead of `sync.Map`.
- **CGO disabled**: builds with `CGO_ENABLED=0` and
  `GOEXPERIMENT=greenteagc`.
- **godotenv**: `internal/cmd/root.go` auto-loads `.env` via
  `github.com/joho/godotenv/autoload`.

### Agent Loop Detection

The `internal/agent/loop_detection.go` module detects when the agent is stuck
in a tool-call loop. It hashes the `(tool_name + tool_input + tool_output)`
pairs in the last 10 steps and flags if any signature appears more than 5
times. The coordinator calls this after each step and cancels the run if a
loop is detected.

### Auto-Summarization Thresholds

Defined in `internal/agent/agent.go`:

- `largeContextWindowThreshold` = 200,000 tokens — triggers summarization
- `largeContextWindowBuffer` = 20,000 tokens — safety margin
- `smallContextWindowRatio` = 0.2 — ratio for small model windows

### Skills System

Skills follow the [Agent Skills](https://agentskills.io) standard. Each skill
is a `SKILL.md` file discovered from configured paths.

- `internal/skills/skills.go`: Skill struct, YAML frontmatter parsing,
  discovery logic.
- `internal/skills/manager.go`: Per-workspace skill state, pubsub for change
  events.
- `internal/skills/catalog.go`: Catalog service for listing/reading skills.
- Built-in skills live in `internal/skills/builtin/` (crush-config, crush-hooks, jq).
- Agent custom skills live in `.agents/skills/`.

### Permission System

The `internal/permission/` package implements a request/grant/deny permission
model for tool execution.

- Supports persistent grants (per tool name) and session-scoped grants.
- Supports "YOLO mode" (auto-accept all permissions via `--yolo` flag).
- Hooks that approve a tool call short-circuit permissions via
  `WithHookApproval` context key.
- Allowed tools can be configured via `crush.json`.

## Build/Test/Lint Commands

- **Build**: `go build .` or `go run .` or `task build`
- **Test**: `task test` (`go test -race -failfast ./...`) or `go test ./...`
  - Run single test: `go test ./internal/agent -run TestCoderAgent`
- **Update Golden Files**: `go test ./... -update`
  - Update specific package: `go test ./internal/ui/model -update`
- **Format**: `task fmt` (`gofumpt -w .`)
- **Lint**: `task lint` (golangci-lint + log capitalization check)
- **Lint with fixes**: `task lint:fix`
- **Modernize**: `task modernize` (applies Go modernize suggestions)
- **Dev mode (profiling)**: `task dev` (sets `CRUSH_PROFILE=true`)
- **Install**: `task install`
- **Generate schema**: `task schema` (writes `schema.json`)

CI runs:
- `build.yml`: `go mod tidy && git diff --exit-code && go build -race ./... && go test -race -failfast ./...` on ubuntu, macos, windows
- `lint.yml`: Reusable workflow from `charmbracelet/meta`, golangci-lint v2.10

## Code Style Guidelines

- **Imports**: Use `goimports` formatting, group stdlib, external, internal
  packages.
- **Formatting**: Use gofumpt (stricter than gofmt), enabled in
  golangci-lint.
- **Naming**: Standard Go conventions — PascalCase for exported, camelCase
  for unexported.
- **Types**: Prefer explicit types, use type aliases for clarity (e.g.,
  `type AgentName string`).
- **Error handling**: Return errors explicitly, use `fmt.Errorf` for
  wrapping.
- **Context**: Always pass `context.Context` as first parameter for
  operations.
- **Interfaces**: Define interfaces in consuming packages, keep them small
  and focused. Prefer accepting interfaces, returning structs.
- **Structs**: Use struct embedding for composition, group related fields.
- **Constants**: Use typed constants with iota for enums, group in const
  blocks.
- **JSON tags**: Use snake_case for JSON field names.
- **File permissions**: Use octal notation (0o755, 0o644) for file
  permissions.
- **Log messages**: Must start with a capital letter (e.g.,
  "Failed to save session" not "failed to save session").
  - This is enforced by `scripts/check_log_capitalization.sh` via `task lint:log`.
- **Comments**: End comments in periods unless comments are at the end of the
  line. Wrap comments at 78 columns.
- **Testing**: Use testify's `require` package. Use `t.Parallel()`, `t.SetEnv()`,
  `t.Tempdir()`. Never manually remove temp dirs.

## Testing Patterns

### Standard Tests

```go
func TestSomething(t *testing.T) {
    t.Parallel()
    // use t.Tempdir() for temporary directories
    // use t.Setenv() for environment variables
    require.NoError(t, err)
    require.Equal(t, expected, actual)
}
```

### VCR (Record/Replay) Tests

Agent tests use `charm.land/x/vcr` to record and replay LLM API calls. This
avoids hitting real APIs during test runs. Cassettes live in
`internal/agent/testdata/` organized by test name and model variant.

```go
func TestCoderAgent(t *testing.T) {
    r, err := vcr.NewRecorder(t.Name()+"/"+modelName, nil)
    require.NoError(t, err)
    defer r.Stop()
    // ... use r.Client for HTTP calls
}
```

To re-record cassettes (after prompt or system changes):
```
task test:record
```

### Mock Providers

For tests that involve provider configurations without making API calls:

```go
func TestYourFunction(t *testing.T) {
    originalUseMock := config.UseMockProviders
    config.UseMockProviders = true
    defer func() {
        config.UseMockProviders = originalUseMock
        config.ResetProviders()
    }()
    config.ResetProviders()
    providers := config.Providers()
    // ... test logic
}
```

### Golden File Tests

UI components use `charm.land/catwalk` for snapshot testing. Golden files
have `.golden` extensions. Update with `go test ./... -update` or
`go test ./internal/ui/... -update`.

### Test Helpers

- `internal/agent/common_test.go`: `fakeEnv` struct and `modelPair` for
  setting up test environments with real or mock providers.
- `internal/config/load_test.go`: Config loading tests with sample config
  files.
- `internal/shell/`: Dedicated test packages for shell command execution.

## Notable Gotchas & Non-Obvious Patterns

1. **`CGO_ENABLED=0` + `GOEXPERIMENT=greenteagc`**: Always set. The build will
   fail without these flags. The `Taskfile.yaml` and CI both enforce this.
   However, `task lint` explicitly sets `GOEXPERIMENT=null` because
   golangci-lint doesn't support it.

2. **Config layering order**: Global config → workspace config → runtime
   overrides. Workspace config (`.crush/crush.json`) has highest file
   priority. Runtime overrides (like `--yolo`) are never persisted.

3. **Config hot-reload**: `ConfigStore.AutoReload()` polls config files for
   changes. During reload, `reloadInProgress` is set to prevent re-entrant
   writes. Snapshot-based change detection via `fileSnapshot`.

4. **Variable resolution timeout**: `config.resolveTimeout` = 5 minutes.
   Shell command substitution in config values can block for up to 5 minutes
   before timing out.

5. **Pub/sub lossiness**: The default buffer is 4096 events per subscriber.
   Streaming token updates use lossy `Publish`; terminal events use
   `PublishMustDeliver` with a 50ms timeout. Check `DropCount()` and
   `MustDeliverDropCount()` when debugging dropped events.

6. **Hook runner is parallelism-safe**: Uses errgroup with concurrency
   control. Each hook gets its own shell environment. Halt (exit 49) from
   any hook stops all running hooks and cancels the tool call. Hook context
   is bounded by the tool call context (not the session context) so slow
   hooks don't outlive the tool call.

7. **Loop detection is hash-based**: Uses SHA-256 of
   `(tool_name + tool_input + tool_result)` pairs. Window size = 10 steps,
   max repeats = 5. Only triggers on actual tool-call steps, not assistant
   text responses.

8. **SessionAgent vs Coordinator**: `SessionAgent` handles a single LLM
   conversation. `Coordinator` manages multiple named agents ("coder",
   "task") and routes prompts to the right agent. There can be multiple
   agents per session but typically there's one "coder" agent per session.

9. **LSP auto-discovery**: Uses `charm.land/x/powernap` for built-in LSP
   defaults (gopls, typescript-language-server, etc.). Users can override
   in `crush.json` LSP config. LSP clients start on-demand (when a file of
   matching type is opened).

10. **Server mode uses Unix sockets/named pipes**: `/tmp/crush-{uid}.sock`
    on Unix, `npipe:////./pipe/crush-{uid}.sock` on Windows. The server
    address is embedded in `CRUSH_HOST` env var for child processes.

11. **Copilot models**: Some Copilot models (gpt-5.2, gpt-5.2-codex, etc.)
    use the Responses API instead of Chat Completions. Check
    `internal/agent/coordinator.go:65-71` for the current list.

12. **Title generation strips think tags**: Agent-generated conversation
    titles strip  ` and ` tags via regex in `internal/agent/agent.go`.

13. **Skills are discovered per-workspace**: Each workspace gets its own
    `skills.Manager` with its own discovery results. Backend server hosts
    multiple workspaces concurrently and must NOT enable global mirroring
    (`WithGlobalMirror`).

14. **sqlc generated code**: DB code is generated. Never edit
    `internal/db/*.sql.go` files directly. Edit the SQL in
    `internal/db/sql/` and run `sqlc generate`.

15. **`mcp.Initialize` is called early**: MCP initialization starts in
    `app.New()` via `go mcp.Initialize(...)` — it's async and non-blocking.

## Formatting

- ALWAYS format any Go code you write.
  - First, try `gofumpt -w .`.
  - If `gofumpt` is not available, use `goimports`.
  - If `goimports` is not available, use `gofmt`.
  - You can also use `task fmt` to run `gofumpt -w .` on the entire project,
    as long as `gofumpt` is on the `PATH`.

## Comments

- Comments that live on their own lines should start with capital letters and
  end with periods. Wrap comments at 78 columns.

## Committing

- ALWAYS use semantic commits (`fix:`, `feat:`, `chore:`, `refactor:`,
  `docs:`, `sec:`, etc).
- Try to keep commits to one line, not including your attribution. Only use
  multi-line commits when additional context is truly necessary.
- Tag format: `v{major}.{minor}.{patch}` (e.g. `v0.72.0`). Created via
  `task release` which uses `svu` for version bumping.

## Working on the TUI (UI)

Anytime you need to work on the TUI, read `internal/ui/AGENTS.md` before
starting work.