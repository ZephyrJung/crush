# Config

> [!NOTE]
> This build of Crush has a single, fixed model provider: the local Ollama
> server at `http://localhost:11434/v1`. Provider credentials, catalogs and
> sign-in flows have been removed. The only two models are:
>
> - large: `qwen3.8:27b-mlx`
> - small: `qwen3.6:35b-mlx`

Crush is configured with Bash via a set of Crush-specific builtin commands. By
default, global config lives at `~/.config/crush/crushrc` on Unix-like systems
and `%USERPROFILE%\.config\crush\crushrc` on Windows. It works like a `.bashrc`:
it runs when Crush starts and configures the agent. Project-local config can be
placed in `./.crushrc` or `./crushrc`.

```bash
# Auto-approve some tools.
permissions allow view edit

# Add an MCP server.
mcp add github \
  --type http \
  --url "https://api.githubcopilot.com/mcp/" \
  --header Authorization "Bearer $GITHUB_TOKEN"
```

Because the config is Bash, you can use logic, `source` other files, and so on.

## JSON config

The older JSON format is still accepted. The generated
[`schema.json`](../../schema.json) describes all available fields. Provider
entries in a JSON config are ignored: the built-in Ollama provider is always
used.

```json
{
  "$schema": "https://charm.land/crush.json",
  "options": {
    "context_paths": ["AGENTS.md"],
    "disabled_tools": ["sourcegraph"]
  },
  "env": {
    "MY_TOOL_TOKEN": "$(op read 'op://my-secret-key')"
  }
}
```

Values in `env` support `$VAR` and `$(command)` expansion; they are applied at
startup before the agent runs.

## Config locations

| Priority | Unix-like                 | Windows                               |
| -------- | ------------------------- | ------------------------------------- |
| 1        | `./.crushrc`              | `.\.crushrc`                          |
| 2        | `./crushrc`               | `.\crushrc`                           |
| 3        | `~/.config/crush/crushrc` | `%USERPROFILE%\.config\crush\crushrc` |

Crush respects the XDG Base Directory Specification, so paths may differ
depending on `XDG_CONFIG_HOME`. Application state (not configuration) is kept
under `~/.local/share/crush` on Unix-like systems and `%LOCALAPPDATA%\crush` on
Windows.

You can override the config and data locations with:

- `CRUSH_GLOBAL_CONFIG`
- `CRUSH_GLOBAL_DATA`
- `CRUSH_DATA_DIR` (overrides the per-project `.crush` directory)

## Security

Both `crushrc` and `crush.json` are trusted code: `crushrc` runs in a full
shell, and any `$(...)` in `crush.json` runs at load time. Do not launch Crush
in a directory whose config you have not reviewed.

## Other settings

See the [main README](../../README.md) for themes, LSPs, MCP OAuth, hooks,
permissions, skills, notifications and attribution settings.
