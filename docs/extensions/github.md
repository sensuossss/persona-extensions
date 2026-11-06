# GitHub

[Documentation](../README.md) · [Source](../../extensions/github/)

This extension brings GitHub into Persona. It is the GitHub connector that Persona's hub used to have built in, moved out of the hub, plus a GitHub page: what needs you, your repositories with their code, and full PR and issue review. The connector id is still `github`. A hub that had its own connector therefore keeps its sign-in, inbox items, knowledge-base conversations, connector switches and inbox options when the extension takes over. While the extension is on, its connector stands in for the hub's own. When the extension is switched off or removed, the hub's own connector comes back.

| Extension point | Contribution | Default | Behavior |
| --- | --- | --- | --- |
| `connectors` | `github` | on | Inbox, knowledge-base archive, assistant tools, gated writes, thread pages and sign-in; see [the connector](#the-connector). |
| `ui` | `github` (`nav_page`) | on | The GitHub page in the apps' navigation; see [the page](#the-page). It can be switched off under Settings → Extensions → GitHub. |

Switching the extension on turns on what the hub already did before (the connector) and the page. Version 0.1.0 installed the page switched off; a hub that installed 0.1.0 keeps that choice after upgrading, so switch it on there by hand.

## The connector

- **Inbox.** It polls GitHub's notifications at the pace GitHub asks for (`X-Poll-Interval`, at least a minute) and keeps one item per PR or issue. Each poll reads up to 10 pages of 50 notifications (only the first while the hour's requests run low). An item is marked read elsewhere only after a poll that read every unread notification. Kinds:
  - `review_requested`, `mention`, `team_mention`, `assign`; an @ or assignment in a Discussion or on a commit is its own item, linked to the repository's discussions or the commit;
  - `author`: new comments or reviews on your own PR or issue;
  - `ci_failed`: a failing GitHub Actions run of yours, followed up until the branch is green, merged or deleted.

  Each item carries a fallback tier from the connector's rules: a review request is "needs you", a bot's comment is "FYI", and a person's ordinary comment falls back to "FYI". Items therefore never wait for a triage model. Your own rules (`triage_rules`) win over the inbox's global rules. Muted repositories, which CI failures to flag and which branches to ignore are connector options. You change them in the app (Settings → Connectors → GitHub), and the hub hands them to the extension.

  What comes in at all is yours to choose (nwverse/persona#3178):
  - The connector's 进收件箱 switch turns GitHub's inbox off as a whole. It is the hub's own switch (Settings → Connectors → GitHub; web and Android also show it on the extension's page).
  - The `inbox` settings name the repositories (only these, or all but these; `owner/*` stands for an owner's every repository) and the kinds that never come in.

  A notification they leave out is passed over before anything about it is read: it makes no inbox item, so nothing is pushed for it. A change applies to what comes after it (the hub restarts the extension, which reads the notifications again). Items already in the inbox are neither removed nor changed, and the extension no longer updates them. The page and the assistant's tools still read every repository.
- **Knowledge base.** PRs and issues you are involved in (`involves:@me`) are archived with their comments and reviews as conversations `github-<owner>-<repo>-<number>`. Each list is read page by page (up to 1,000 comments, reviews and inline comments per thread), so a busy thread is kept whole. The first run looks back 30 days.
- **Assistant tools.** These read without asking: `github_search`, `github_read`, `github_diff`, `github_checks`. `github_comment` and `github_review` are writes. They go through the hub's gate and are confirmed on the phone by default. Replying from an inbox item is the user's own send.
- **Thread page.** An inbox item opens as the PR's or issue's page in the app: comments, reviews, line comments with their diff, timeline events and failed checks with the end of their logs.
- **Sign-in.** Choose one of the two under Settings → Connectors → GitHub:
  - **A computer's `gh` login.** The hub asks that computer for the token through `connector.credentials.gh_token`. This needs the extension's `gh_login` permission. The token stays in the extension's memory: it is never stored, logged or shown.
  - **The device flow.** The token is sealed in the hub's connector secret.

  The hub keeps the choice and hands it to the extension (`connector.auth.set`).

## The page

The page reads with the connector's own sign-in, so it shows only what the connector may read. Its layout is the one both code-hosting extensions share (nwverse/persona#2951). The list has four tabs; it opens on 待我 review when something waits for you, else on 我提的:

| Tab | What it lists |
| --- | --- |
| 待我 review | Open PRs requesting your review (`is:open is:pr review-requested:@me archived:false`), with CI rollup and review decision. |
| 我提的 | Your open PRs, with CI rollup (passed, failed, running), review decision (approved, changes requested) and draft state. |
| 我参与的 | Open issues and PRs assigned to you or mentioning you, listed once each. Its filter also has 通知 (unread notifications with their reason; the latest ones when nothing is unread) and 关注的动态 (recent activity from repositories and people you follow, `received_events`). |
| 仓库 | Repositories: recently pushed, your own, starred, or one of your organizations' (the filter); the search box searches all of GitHub. |

Every row opens something: a PR or issue opens its page, a repository opens the repository, anything else opens on GitHub. A GitHub link pasted into the search box opens what it names.

**A repository** has its description, stars, forks, language and default branch, then five tabs: 代码 (the branch filter with up to 20 branches, the directory with folders first, moved through in place so the navigation stack stays shallow, and the README), PR and Issue (open or closed, searchable, 30 a page), 提交 (the branch's history) and 分支 (all branches, searchable; a tap shows the code at it). A file opens with its content: Markdown drawn (or its source), code in fenced blocks with its language, 400 lines a page. Binary files and files over 1 MB are not shown.

**A PR** has four tabs:

- 概览: state, author, branches (with the fork), files, additions, deletions, commits, whether it can be merged and why not, labels, assignees and milestone. When checks fail, the failed ones come first with the error lines of the first failed Actions log and a button to run the failed jobs again. Then the description, each reviewer's latest review and the requested reviewers and teams, the checks' counts, the whole conversation (comments, reviews with their verdict, comments on lines opening the file, commits pushed and timeline events; the latest 30, earlier ones on 更早的), and the buttons to comment, review and merge.
- 改动: the changed files (100 a page; the page holds 280, the rest are on GitHub), each with its status, additions, deletions and comment count. A file opens its diff: a section per hunk, each line with its number (the new file's; a removed line the old file's), and every conversation on a line right under that line, with its replies, whether it is resolved (GraphQL) and a reply button. Conversations on lines the PR no longer has come at the end with the code they were on. 600 lines a page.
- 提交: its commits (GitHub lists up to 250).
- CI: the head commit's check runs and commit statuses, failed first; the tab's title counts the failed ones. A check run opens with its annotations, its steps that did not pass, the error lines picked from its log and the log up to its last error. A log can take a minute to come from GitHub's storage: the page waits a few seconds, then shows that it is reading and draws again when the log has come.

**An issue** has the same overview without the PR's parts.

**Writes.** From the page you can comment, comment on a line, reply to a conversation, submit a review (comment, approve, request changes), merge (squash, merge commit, rebase) and run failed Actions jobs again. Each one is filled in, then previewed word for word with where it goes and who will see it, and sent only with the preview's button, which the app asks about once more. A comment on a line is checked against the diff first. A merge is sent with the head commit the preview showed, so a push after the preview stops it. Writes are at most 2,000 characters (a field's limit). These are the user's own taps; the assistant's writes still go through the hub's gate.

**Reading.** A page the hub would wait too long for (a big PR's first read: its comments, reviews, events, checks and commits) answers after 12 seconds with a loading page, goes on reading in the background and has the app draw it again when done (`ui.invalidate`; `refresh_after` for apps and hubs without it). The hub gives `ui.render` 20 seconds. Every list is read with `If-None-Match`, so what has not changed costs nothing of the hour's requests. The five tabs are cached for a minute; the refresh button reads again. When the hour's rate limit is nearly used up, the tabs show what they read before and keep the remaining requests for the inbox.

**Rendering.** Apps of UI version 4 and later (nwverse/persona#2951, #3136) get the code-hosting components: 改动 is a split view with the file tree beside one diff component per file (line numbers on both sides, unified or side by side, syntax highlighting, conversations at their lines), and a file in the repository is a code component. The first 10 files (or 3,000 lines) come with their lines; the app loads the rest as it scrolls to them. UI version 5 adds:

- Hidden lines between hunks, 20 at a time or all: the file is read at the PR's head (in the fork when the head is one) and handed to the app once.
- Viewed: GitHub's own Viewed mark, read and set through GraphQL, so it is the same on github.com.
- Comments on a line or a range of lines (dragged over), alone or as a draft of your pending review (GitHub's pending review, nobody else sees it). Drafts show at their lines; the review bar at the foot sends them with a summary as a comment, an approval or a request for changes, or throws them away.
- Suggestions (```` ```suggestion ```` blocks), resolving and reopening conversations, links to a file's lines.

Older apps keep the earlier rendering: code and diffs are fenced Markdown blocks (```` ```diff ````, ```` ```go ````), files and comments are rows. Descriptions, comments and READMEs are cleaned to the Markdown the hub accepts: images become links, relative links resolve against the repository, HTML and reference links are rewritten, and a comment the hub would still refuse is shown as text.

Apps from before UI version 3 get rows instead of the segmented tabs, a search field instead of the search box, plain text instead of Markdown and a back button.

Limits of declarative views:

- An extension view cannot open a Persona conversation or an inbox item. The detail page names the knowledge-base conversation instead.
- Apps before UI version 4: lines of a diff cannot be tapped, a comment on a line is written with its line number.
- Images are limited to avatars.

## Configuration

[Manifest and settings schema](../../extensions/github/persona-extension.json)

| Setting | Meaning |
| --- | --- |
| `triage_rules` | Your own rules for sorting GitHub's inbox items, in your words; empty uses the inbox's global rules. |
| `inbox.repos` | Only these repositories come into the inbox: `owner/name` or `owner/*`, any case. Empty takes every repository. |
| `inbox.exclude_repos` | These repositories never come in, after `inbox.repos`; same patterns. |
| `inbox.skip_kinds` | These kinds never come in (`review_requested`, `mention`, `team_mention`, `assign`, `author`, `ci_failed`). To rank a kind lower without leaving it out, write it in `triage_rules`; to flag fewer CI failures, pick the connector's CI option. |
| `host` | GitHub's host, `github.com` by default. GitHub Enterprise is not finished: only `github.com` works today. |
| `proxy` | An `http://[user:pass@]host:port` proxy for everything sent to GitHub (sign-in, API, CI logs). A malformed value fails every request rather than going direct. Empty uses the environment's proxy, or goes direct. A hub that had `github.proxy` / `github.auth.host` in its own configuration moves them here when the extension takes over. |

Network access: `github.com`, `api.github.com`, `avatars.githubusercontent.com`, and the hosts GitHub redirects CI logs to (`*.actions.githubusercontent.com`, `*.blob.core.windows.net`).

## Protocol additions

Besides the connector protocol's methods, the program answers these. The hub calls them only for the capabilities declared (`options`, `settle`, `machines`), and calls `auth.set` and `options.set` after every start:

- `connector.options.get`, `connector.options.set`: the user's inbox options;
- `connector.inbox.settle`: closing open items the rules now consider over;
- `connector.machines.online`: a computer connected, so its `gh` login is tried again;
- `connector.auth.get`, `connector.auth.set`: where the sign-in comes from.

It also calls the hub's `connector.credentials.gh_token`. A hub without it makes the computer sign-in report that the hub is too old. The device flow works on any hub that runs the extension.

## Tests

From the repository root:

```sh
go test ./extensions/github/...
```

The tests cover:

- the connector's own suite, carried over from the hub: inbox rules, CI follow-up, archive, tools, writes, thread page, device flow, proxy and token scrubbing;
- the protocol additions;
- the fallback tier and rules on every inbox item;
- the page's five tabs against a fake GitHub, checked as the apps read views;
- search, caching and refresh, the detail page, signed-out and older-app views;
- manifest/program agreement and the SDK's conformance check.
