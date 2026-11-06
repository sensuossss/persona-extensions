# Installation

[Documentation](README.md) · [Development](development.md)

## Register the public market

In the Persona app, open **Settings → Extensions → Extension markets → Add** and enter `github:nwverse/persona-extensions`. Choose an extension from the market to install.

The command-line equivalent, on the hub's machine, is:

```sh
personad extension market add github:nwverse/persona-extensions
```

This repository is public: no GitHub token is required. Tokens mentioned by the generic market CLI are for private markets, not this one.

The market indexes released extensions; it is not a directory listing of every example or open PR. Packages are GitHub Release assets with pinned SHA-256 hashes. The hub records the market's public signing key and verifies subsequent indexes. A missing or invalid signature, or a changed key, prevents installation and upgrades. See [market signing](releases.md#market-signing).

If a lookup fails, first check that the extension has a release in this market and that the hub meets its manifest's `engines` requirements. If verification fails, do not bypass it or silently trust a replacement key; ask the market maintainer to resolve the index/signature problem.

## Install a local package

Run from the repository root with the toolchain described in the [development guide](development.md):

```sh
scripts/package.sh linuxdo
```

This writes `dist/linuxdo-<version>.tar.gz` and `dist/linuxdo-<version>.tar.gz.sha256`. Replace `<version>` below with the version in [the manifest](../extensions/linuxdo/persona-extension.json), then copy the archive to the hub's machine and run:

```sh
personad extension install file:/path/to/linuxdo-<version>.tar.gz
```

The package includes compiled binaries for the platforms listed in its manifest; the hub does not need a Go toolchain or an extra runtime. Packaging is local; it does not create a release or register the package in the market.

## Older hubs

Hubs from before the Plugin → Extension rename use `personad plugin …` instead of `personad extension …`, for example:

```sh
personad plugin install file:/path/to/linuxdo-<version>.tar.gz
```

Both `persona-extension.json` and the compatible `persona-plugin.json` are shipped, generated from the one source manifest with the same contents. New hubs prefer the former; old hubs read the latter. Some extensions also declare a minimum hub version in `engines.persona`; renaming the command does not bypass that requirement.

Legacy protocol names and UI placements, such as `plugin_protocol` and `plugin_page`, are intentional compatibility identifiers. See [reference](reference.md#compatibility).

## Extension-specific setup

- [GitHub](extensions/github.md): sign in through **Settings → Connectors → GitHub**, either by sharing a computer's `gh` login (that computer must be online and the extension uses its `gh_login` permission) or through the device flow. The hub must reach `github.com`, `api.github.com` and GitHub's avatar and CI-log hosts.
- [Home Assistant](extensions/homeassistant.md): fill in the address and a long-lived access token in its settings; the hub must reach Home Assistant. A local package starts sandboxed and needs `personad extension sandbox homeassistant off`, see [Network and sandbox](extensions/homeassistant.md#network-and-sandbox).
- [LINUX DO](extensions/linuxdo.md): no account; the hub must reach `linux.do`. The in-app reader fetches on the hub, while opening the original web page also requires network access on the device.
- [V2EX](extensions/v2ex.md): no account; the hub must reach `www.v2ex.com`. The in-app reader fetches on the hub, while opening the original web page also requires network access on the device.
- [Markets](extensions/markets.md): no account; the hub must reach `www.okx.com`, and for stocks `qt.gtimg.cn`, `smartbox.gtimg.cn`, `web.ifzq.gtimg.cn` and `ifzq.gtimg.cn`.
- [Telegram](extensions/telegram.md): sign in through **Settings → Connectors → Telegram**. The hub must reach Telegram data centers directly over TCP. Local or other non-official installation sources may require disabling the extension sandbox; follow the [Telegram network instructions](extensions/telegram.md#network-and-sandbox).
- [Bypass auto-allow](extensions/claude-bypass-allow.md): needs a hub that runs extension programs on computers (`node`). Nothing happens until you switch on a computer in its settings; it then edits that computer's Claude Code `settings.json`.
- [mnemo](../extensions/mnemo/README.md): no account. Install [mnemo](https://github.com/szupzj18/mnemo#install) on the computer that runs the assistant, where Persona finds it on `PATH` or in `~/bin` (or through `PERSONA_MNEMO_BIN`). Only the Claude assistant gets its tools.
- [Feishu (LarkAgentX adapter)](extensions/larkagentx.md): experimental and Feishu-only. The hub package is not enough: it also needs a separate Linux/macOS companion with Python 3.9+ and the pinned LarkAgentX user-installed in a virtual environment, reached over an SSH command you configure. The guide walks through the local-package install, helper extraction, chat/sender allowlists and QR/import sign-in. It does not replace OpenLark.

Do not paste credentials into repository files or installation examples. Configure accounts through the hub/app's protected credential flow.
