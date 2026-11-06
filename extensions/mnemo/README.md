# mnemo

Let the Persona assistant search past coding-agent sessions. When [mnemo](https://github.com/szupzj18/mnemo) is installed on the computer that runs the assistant, the Claude assistant gets mnemo's MCP server and its `mcp__mnemo__*` tools: full-text search over the sessions that Claude Code, Codex, OpenCode, Pi, Antigravity CLI and Grok CLI have written to disk, and the messages around each hit. Questions like "how did we fix this last time?" can then be answered from the original session instead of from memory.

The extension runs no program. Its manifest only declares an `mcp_servers` contribution; Persona starts `mnemo mcp` as one more MCP server of the Claude assistant. Codex and runtime assistants do not get it.

- [Manifest](persona-extension.json)
- [Installation](../../docs/installation.md)
- [Development and tests](../../docs/development.md)

## Requirements

- A hub with the `mcp_servers` extension point (`engines.persona >=202611051918`).
- mnemo on the computer that runs the assistant. Follow [mnemo's install instructions](https://github.com/szupzj18/mnemo#install); `mnemo --help` must list the `mcp` command.
- Persona looks for `mnemo` on `PATH`, then in `~/bin`. mnemo's installer links it into `~/.local/bin`, which a background service's `PATH` often lacks. If the assistant does not get the tools, link it into `~/bin` (`ln -s ~/.local/bin/mnemo ~/bin/mnemo`) or set `PERSONA_MNEMO_BIN` to its full path in the environment Persona runs in.

Without mnemo, nothing is added and the assistant works as before. To leave mnemo out on one computer, set `PERSONA_MNEMO_BIN` to `off` there.

## Tools

| Tool | Access |
| --- | --- |
| `search_sessions` | Runs without asking |
| `get_context` | Runs without asking |
| `get_session` | Runs without asking |
| `list_recent_sessions` | Runs without asking |
| `reindex` | Denied: never callable |
| Any other mnemo tool (such as `list_skills`, `skill_usage`) | Follows the conversation's permission mode: asks on the phone by default, runs without asking under `bypassPermissions` |

The deny rule applies to `mcp__mnemo__reindex` whatever mnemo's own settings say, including when you have also added mnemo to Claude Code yourself. mnemo still refreshes its index incrementally when the server starts.

## Privacy

- Everything stays with mnemo. Its index is a local SQLite file on that computer; searches of other devices go only to the devices you registered in mnemo yourself (for example `devbox-a`), over your own SSH access.
- Persona passes no credentials and adds nothing to mnemo's environment, and it does not read mnemo's configuration or index.
- What the tools return (session snippets, which can contain commands and tool output) becomes part of the assistant's conversation, the same as files the assistant reads.

## Example

Ask the assistant: "How did we fix the connection-pool timeout last week?" It calls `search_sessions`, reads the matching messages with `get_context`, and answers with the session and the fix.
