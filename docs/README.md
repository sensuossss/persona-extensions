# Documentation

[Project overview](../README.md) · [Contributing](../CONTRIBUTING.md)

## Using extensions

- [Installation](installation.md): register the public market, install local packages and handle older hubs.
- [GitHub](extensions/github.md): the inbox, knowledge-base archive, assistant tools and sign-in of the GitHub connector (moved out of the hub), and a GitHub page: what needs you, repositories and their code, and PR / issue review.
- [Home Assistant](extensions/homeassistant.md): areas, devices, controls and history from your own Home Assistant; confirmed assistant writes, writable domains and the sandbox.
- [LINUX DO](extensions/linuxdo.md): public topics, filtering, request limits and the app view.
- [V2EX](extensions/v2ex.md): public hot, latest and followed-node topics, filtering, request limits and the app view.
- [Markets](extensions/markets.md): cryptocurrency (OKX) and A-share, Hong Kong and US stock (Tencent) watchlist on the main navigation, price and volume charts, assistant tools and rate limits.
- [Telegram](extensions/telegram.md): login, inbox, archives, network requirements, tools and the `tg` CLI.
- [Bypass auto-allow (Claude Code)](extensions/claude-bypass-allow.md): auto-allow Claude Code's remaining Bash, read-only and subagent prompts in bypassPermissions sessions on chosen computers; deletion and overwriting ask unless inside temporary or work directories (or a setting allows them all); force pushes, permission changes and your own ask/deny rules still ask; audit log and limits.
- [mnemo](../extensions/mnemo/README.md): let the Claude assistant search past coding-agent sessions through mnemo's MCP server, when mnemo is installed on its computer; read-only tools only.
- [Feishu / LarkAgentX adapter](extensions/larkagentx.md): non-redistributing user-installed companion, headless QR/import login, exact allowlists, always-confirmed text sends and experimental validation limits.

## Developing and maintaining extensions

- [Development](development.md): repository layout, adding an extension, local checks and packaging.
- [Reference](reference.md): manifests, configuration, process protocol, SDK ownership and compatibility.
- [Releases and signing](releases.md): release automation, index format, verification and key management.

This repository documents its public extensions and build/release workflow. The [local reference](reference.md) links to the published SDK contracts and schemas; it does not rely on access to the host implementation repository.

## Example extensions

- [Showcase](extensions/showcase.md): a local-only component catalog, widget and shortcut examples, view snapshots and older-app fallbacks.
- [Starter kit and starter pack](extensions/starter-kit.md): runtime-free skills, assistant instructions, quick prompts, triage templates and a theme; a pack that installs starter-kit and LINUX DO together. These require a hub with declarative-contribution support (`engines.persona >=202611042338`).

These examples are available in `main` and the market. Keep both project READMEs and this index aligned when adding extensions.
