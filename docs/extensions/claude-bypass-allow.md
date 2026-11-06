# Bypass auto-allow (Claude Code)

[Documentation](../README.md) · [Source](../../extensions/claude-bypass-allow/)

Claude Code in `bypassPermissions` mode (`--dangerously-skip-permissions`, "bypass permissions on") still stops on some calls and waits for someone to choose **Yes**. Persona then shows the agent as waiting for you, and an unattended run, often a subagent, stalls until someone answers or Claude Code denies the call after two minutes. On the computers you choose, this extension adds a Claude Code `PermissionRequest` hook. It allows those prompts only in `bypassPermissions` sessions, only for Bash, the read-only tools and subagents, and never for a Bash command that deletes or overwrites data, forces a push or changes permissions. A `Read` rule of yours covers `Glob`, `Grep`, `LS` and `NotebookRead` too. In every other case, it stays silent and Claude Code asks as usual.

## What still prompts in bypass mode

Measured with Claude Code 2.1.308 in a throwaway project:

| Command | Prompts in bypass mode |
| --- | --- |
| `cd view && rm -f sub/*` (a relative glob after `cd`) | Yes: "Dangerous rm operation on statically-unresolvable target" |
| `for d in …; do rm -rf "$t"; done` (a target built at run time) | Yes, same check |
| A Bash command matching your own `permissions.ask` rule, such as `Bash(touch:*)` | Yes: "Permission rule … requires confirmation" |
| A 12,000-character one-liner, a heredoc, `eval`, a `for` loop, `X=view; rm -rf "$X"/x` | No |
| Read, Glob or Grep of a path outside the working directories, when reads outside them are blocked | Yes: "Read outside the working directories" |

Length alone does not trigger a prompt. Claude Code has a small set of safety checks that bypass mode does not skip. Its build calls them *bypass-immune*. For Bash, the main one is *dangerous removal*: an `rm` or `rmdir` whose target cannot be resolved before the command runs. Examples are a glob after `cd`, a variable or `$(...)` target, and an `rm` inside a `bash -c` script that Claude Code cannot parse. Claude Code runs `PermissionRequest` hooks for these prompts. The hook input includes `permission_mode`, and a hook's `allow` is honoured. When several hooks are configured, the first `allow` wins, so this hook does not wait for slower ones. Persona's own hook, which sends the prompt to your phone, is one of them.

## What it allows, and what it leaves alone

The hook answers `allow` only if all of these hold:

- The session's `permission_mode` is `bypassPermissions`, and the tool is `Bash`, a read-only tool (`Read`, `Glob`, `Grep`, `LS`, `NotebookRead`) or `Agent` (`Task` in older versions). A subagent's own calls come to the hook one by one and are judged the same way. Other modes (`default`, `acceptEdits`, `plan`, `auto`) and other tools, including MCP tools, `Write`, `Edit`, `MultiEdit`, `NotebookEdit` and `Workflow`, are never answered. The settings entry's matcher lists exactly these tools.
- For Bash, the command is not one that stays with a person. These are: deletion (`rm`, `rmdir`, `unlink`, `shred`, `rimraf`, `find -delete` (and whatever `find -exec` runs), `git clean`, `git rm`, `git branch -d`/`-D`/`-M`/`--delete-merged`, `git tag -d`/`-f`, `git stash drop`/`clear`, `git worktree remove`, `git filter-branch`/`filter-repo`, `git reflog expire`, `git update-ref -d`/`--stdin`, `rsync --delete`/`--del*`, `docker rm`/`prune`, `docker compose down -v`, `kubectl delete`, `terraform destroy`, `aws … delete-*`/`s3 rm`/`s3 rb`/`--delete`, `gcloud … delete`, `az … delete`, `gh … delete`, `gh repo archive`, `gh pr merge --delete-branch`, `gh api -X DELETE`, SQL `DROP`/`TRUNCATE`/`DELETE FROM`); overwriting data (`>`, `>|`, `N>` or `&>` into a file outside `/dev/` and the scratch directories, `tee` without `-a` into such a file, `sed -i`/`perl -i`, `ln -f`, `mv`, `cp -f`, `dd`, `mkfs*`, `truncate`, `git reset --hard`, `git checkout -f`/`--`/`.`, `git checkout <commit> <path>`, `git checkout -B`, `git switch -C`/`--orphan`, `git restore`, `git switch --discard-changes`); a forced or deleting push (`--force*`, `-f`, `-d`, `--mirror`, `--delete`, `--prune`, `+ref`, `:ref`); and permission changes (`chmod`, `chown`, `chgrp`, `install -m`/`-o`/`-g`, `chattr`, `setfacl`, `usermod`, `passwd`, `visudo`, `setcap`, SQL `GRANT`/`REVOKE`/`ALTER USER`, `gh secret`/`variable`/`ssh-key`/`gpg-key`, `gh repo edit --visibility`, `gh repo deploy-key`, a `gh api` write to collaborators, permissions, branch protection, teams, members, keys, secrets, hooks or environments or that sets `visibility`/`private`, a `gh api graphql` mutation, `aws iam` and `put-*-policy`/`put-*-acl`, `gcloud … iam-policy`, `az role`, creating or changing Kubernetes roles and bindings). The `gh` checks read the command after gh's `-R`/`--repo` flag too (`gh -R owner/repo secret set X`). A shortened git long option counts like the full one, as git reads it (`git reset --har`, `git push --dele`). The command is split the way a shell splits it. Its `$'…'` and `$"…"` quotes are decoded (`$'\x72m'` is `rm`), braces are expanded (`{rm,-rf,x}`, `r{m,}`), a comment is skipped, and a program word with a glob in it (`/bin/r?`) counts as each program it can match. A word counts as a program when it comes first or after a wrapper (`sudo`, `xargs`, `env`, `timeout`, `find -exec`, `unshare`, `taskset`, `coproc`, `do`, `then` and the like), so `grep -rn truncate src/` and `git mv a b` go through and `sudo -b rm x` does not. Every part of a list, pipeline or substitution counts. A quoted argument is read again as a command when a shell or command runner runs it (`bash -c`, `sh -c`, `csh -c`, `tcsh -c`, `pwsh -c`, `ssh host '…'`, `eval`, `env -S`, `flock -c`, `script -c`, `sg -c`, `npx -c`, `trap`, `git rebase -x`, `git submodule foreach`) or when it holds a substitution (`$(…)`, backticks), and commands run by `git bisect run` or a `git -c` value that is a command (`alias.*='!…'`, `core.sshCommand`, `core.editor`, `diff.external` and the like) count, and so does a variable git runs the same way when it is set on the command (`GIT_SSH_COMMAND`, `GIT_EDITOR`, `GIT_SEQUENCE_EDITOR`, `GIT_EXTERNAL_DIFF`, `GIT_PAGER`, `PAGER`, `EDITOR`, `VISUAL`, before the program or after `env`), so `bash -c 'rm -rf x'` and `git commit -m "$(rm -rf x)"` stay with you. Output written with `>`, `>|`, `N>` or `&>` overwrites a file and stays with you, except into `/dev/…` or `/tmp/…` (read after `..` is resolved, so `/tmp/../etc/hosts` stays with you); `>>` appends and goes through. SQL counts only in a command that runs a SQL client (`psql`, `mysql`, `sqlite3` and similar), including a heredoc fed to it. Free text is not read: `git -m`/`--message`, and `gh --title`/`--body`/`--notes`, so `git commit -m 'drop rm wrapper'` goes through; neither is the body of a heredoc fed to anything but a shell (`sudo -s`, `sudo -i` and `doas -s` start one) or a SQL client, such as `cat > /tmp/x <<EOF`. Only a `<<` the shell reads as a heredoc starts one: after a `<<` inside quotes (`echo 'usage: x <<FILE'`), backticks, a comment or arithmetic (`$((1<<n))`), the lines are still read as commands, and a body starts only after the line the command ends on. A command hidden in a variable, a script file or another program's input is not seen, nor is a deletion done inside a script language (`python3 -c 'shutil.rmtree(…)'`, `perl -e 'unlink …'`).
- A deletion or overwrite whose targets are all in temporary or work directories goes through anyway (next section). With the `allow_destructive` setting on, every deletion and overwrite goes through. A forced push, a permission change, disk formatting (`mkfs*`, `wipefs`, `fdisk`, `parted`, `dd of=/dev/…`), a `gh api graphql` mutation and a command the hook cannot read still ask.
- The extension's program is running on that computer. It refreshes a heartbeat every minute. If the heartbeat is more than five minutes old, the hook allows nothing. This covers the case where the program was killed before it could remove its entry.
- None of your own `ask` or `deny` rules could apply to the call. For tools other than Bash, any rule naming the tool counts for every call of it, whatever its pattern: `Read(./secrets/**)` keeps every Read prompt with you, and a rule for `Task` counts for `Agent`. For Bash rules: The hook reads managed settings, the config directory's `settings.json` and `settings.local.json`, and `.claude/settings.json` and `.claude/settings.local.json` in the session's working directory and each parent. A rule counts when the literal start of its pattern appears anywhere in the command, either as written or after quotes, backslashes and line continuations are removed and each run of whitespace becomes one space. For example, `Bash(git push:*)` counts for `cd x && git push`, `git  push` and `'git' push`, and a bare `Bash` counts for everything. Flags in another order are not matched: `Bash(rm -rf:*)` does not count for `rm -fr` or `rm -r -f`, and Claude Code 2.1.318 does not ask for those either. This errs toward asking. Claude Code applies `deny` rules before it asks, so those calls never reach the hook; they are checked anyway.

The hook cannot see rules passed on the command line (`--settings`, `--allowedTools`/`--disallowedTools`), rules added in the session with `/permissions` and not saved, or project settings outside the session's working directory and its parents. If an `ask` rule exists only in one of those places, Claude Code's prompt for it will be auto-allowed in bypass mode. Put rules you depend on into a settings file.

The hook also answers other safety checks, such as Claude Code's isolated-peer-machine and read-block checks, when they are raised by one of the tools above. Prompts that Claude Code marks as approvable only by a person, such as edits to its own settings files or a classifier timeout in `auto` mode, ignore a hook's `allow`; this extension does not try to work around them. The `Run a dynamic workflow?` prompt is for the `Workflow` tool, which the hook never answers.

Before 0.2.0, the hook answered every Bash prompt in bypass mode, including dangerous removals, and nothing else.

This floor only decides which of Claude Code's own prompts the hook leaves to you. In `bypassPermissions` mode Claude Code runs most commands without asking at all, including a `git push --force` or an `rm` whose target it can resolve; the hook never sees those calls and cannot stop them.

## Deletions in temporary and work directories

An `rm`, `rmdir`, `unlink`, `shred`, `rimraf`, `trash`, `truncate`, `mv`, `cp -f`, `ln -f`, `tee`, `find -delete`, `rsync --delete` or output redirect goes through when everything it changes is strictly under one of these, after symlinks are resolved:

- `/tmp`, `/private/tmp`, `/var/tmp`, `/var/folders`, `/private/var/folders` and `$TMPDIR`;
- the session's working directory, when it is a project: not `/`, the home directory or a parent of it, another person's home, a hidden directory in the home directory (`~/.ssh`, `~/.config`) or a system directory (`/etc`, `/usr`, `/var`, `/opt` and the like);
- `~/ag-work`, `~/wt`, `~/tmp` and `~/scratch`;
- Claude Code's worktrees, `…/.claude/worktrees/<name>`.

A root itself never counts (`rm -rf ~/ag-work`, `rm -rf /tmp`), nor does anything in a `.git` directory. A glob is read only in the last name and never as `.` or `..` (`rm -rf /tmp/x/*` goes through; `/tmp/*/x`, `/tmp/.*` and `/tmp/*/` ask). A target behind a symlink is read where the link leads, and a command that makes links or mounts (`ln`, `mount`) asks.

The check reads the variables the same command sets, so `S=/tmp/v1; for v in e1 e2; do rm -rf $S/$v; done` goes through. It errs toward asking. A variable counts only when every place the command sets it is a plain assignment or a `for … in` list at the top level, between `;` or newlines (not after `&&`, in an `if`, a loop body, a function or a pipeline), with a value that has no quote, space, glob or expansion. It does not count when the name appears anywhere else (`read S`, `export S`, `S+=x`, `printf -v S`), when the command has a command substitution, runs a shell or changes `IFS`, or when it is used before it is set. `$HOME`, `~` and `$TMPDIR` are the hook's own unless the command sets them. A relative target is read against the session's working directory and every directory the command `cd`s to, and counts only when it is scratch against each; a `cd` it cannot read (`cd`, `cd -`, `cd $UNSET`) leaves relative targets with you. A program run through `xargs`, `find -exec`, `ssh`, `eval`, `env -C`, `sudo -D` or a container asks; plain wrappers such as `sudo`, `nice`, `timeout` and `do` do not change anything. A command a sourced script changes behind the hook's back (a `readonly` variable, an alias) is not seen.

The audit entry says why: `scratch: rm` for a deletion in these directories, `setting: deletion: rm` for one the setting let through.

## Persona's own approval card

In a session that Persona started, Persona's hook also receives each `PermissionRequest` and puts a card on your phone. Claude Code does not stop that hook when another hook allows the call, so the card would stay up and the agent would be shown as waiting for you even though the command already ran. After each allow, this hook therefore tells Persona's node on that computer through its socket (`~/.persona/node.sock`, kind `hook.decided`, with the same request input). A node that knows `hook.decided` (nwverse/persona#2648) then shows no card, or withdraws the card it already showed. On an older node, the card stays until it is answered or times out; the command runs either way.

## Settings and switches

| Setting | Meaning |
| --- | --- |
| `machines` | The computers where the hook is installed, one switch per macOS or Linux computer. No computer is on after installation; turning one on asks for confirmation first. |
| `allow_destructive` | 删除和覆盖也自动允许 (Auto-allow deleting and overwriting too). Off by default. When on, Bash commands that delete or overwrite data go through wherever their targets are. Forced pushes, permission changes, disk formatting and your own ask and deny rules still ask. The program writes it to `options.json` in its data directory when it starts; the hub restarts it when the setting changes. |
| `config_dirs` | Claude Code config directories that receive the hook. Defaults to `~/.claude`; `~` is that computer's home directory. Add each directory you use with `CLAUDE_CONFIG_DIR`. A missing directory is skipped and shown on the extension page. |

The extension does nothing until a computer is switched on. You can turn it off for one computer with its switch, or for every computer by disabling the extension. In either case, the program removes its entry as it stops. Uninstalling or upgrading does the same.

## The settings entry

The program adds one group to `hooks.PermissionRequest` in `<config dir>/settings.json`. If the file is a symlink, it edits the file the link points to:

```json
{"matcher": "Bash|Read|Glob|Grep|LS|NotebookRead|Agent|Task", "hooks": [{"type": "command", "timeout": 10,
  "command": "'<package>/bin/claude-bypass-allow-<platform>' hook --config-dir '<config dir>' --data '<data dir>' --persona-extension=claude-bypass-allow"}]}
```

The trailing `--persona-extension=claude-bypass-allow` tag identifies the entry. The program changes only entries with that tag. Other keys and other tools' hooks keep their order and values, and the file keeps its indent. Before each change, it copies the previous file to `settings.json.claude-bypass-allow.bak`. A file that is not a JSON object is left alone and reported as an error. A session that was already running when you turned a computer on may not pick up the hook until it restarts.

## Audit log

Each auto-allow appends one JSON line to `audit.jsonl` in the program's data directory on that computer (`~/.persona/node-plugins/claude-bypass-allow/data/`, mode 0600). The log keeps about the newest 1 MB across two files. Each entry records the time, session id, subagent id (when present), working directory, config directory, the tool, the call's full length, and the call itself: the command for Bash, otherwise the tool's name and what it is about (a path, a pattern, a subagent's type and description). Before the command is stored, it is put on one line, cut to 240 characters, and has likely secrets replaced with `***`: `NAME=value` where the name suggests a token, password or key; `Bearer`/`token` values; `--password`/`--token` flags; `-p` passwords for `mysql` and `sshpass`; the password in `curl -u user:password`; credentials in URLs; and opaque strings of 32 or more characters. This masking is a heuristic, so treat the log as sensitive.

The extension's page in **Settings → Extensions** lists, for each switched-on computer, whether the hook is installed in each config directory and the 30 newest auto-allows.

## Permissions

- **Runs a program on the hub**: only to draw the extension page. It reaches the computers through `nodes.call`, has no network access and holds no hub token.
- **Runs a program on the computers you choose** (`node`), with `files` access. It edits Claude Code's `settings.json` outside its own data directory. It needs no network access and no secrets.

## Install

Use the [public market](../installation.md), or package locally from the repository root:

```sh
scripts/package.sh claude-bypass-allow
go test ./extensions/claude-bypass-allow
```

The tests cover the hook's decisions for each tool, the commands that stay with a person, deletions in scratch directories and the variables they use, the `allow_destructive` setting, rule matching, redaction, the audit log, editing settings.json without changing anything else, the program's install and cleanup, and the page in three languages.
