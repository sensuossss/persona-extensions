package main

import (
	"path"
	"regexp"
	"strings"
)

// The floor: Bash commands that stay with a person even in bypassPermissions mode, because they delete or overwrite
// data, force a push, or change permissions (local or online). When Claude Code asks about such a command, the hook
// stays silent and the person answers. It reads the command roughly as a shell would and errs toward asking: every
// part of a list, pipeline or substitution counts; output redirected into a file counts as overwriting it (except
// under /dev/ and /tmp/); a quoted argument is read again as a command when a shell runs it (bash -c '…', ssh host
// '…', eval) or when it holds a substitution ($(…), `…`); SQL counts when a SQL client is in the command. It skips
// free text: commit messages (git -m), pull request titles and bodies (gh --title, --body), and the body of a
// heredoc fed to anything but a shell.
//
// What it cannot see: a command hidden in a variable, a script file, base64 or another program's input.

// alwaysPerson are programs that delete, overwrite or change permissions whatever their arguments.
var alwaysPerson = map[string]string{
	"rm": "deletion", "rmdir": "deletion", "unlink": "deletion", "shred": "deletion", "srm": "deletion",
	"trash": "deletion", "trash-put": "deletion", "userdel": "deletion", "groupdel": "deletion",
	"dd": "overwrite", "truncate": "overwrite", "wipefs": "overwrite", "fdisk": "overwrite", "parted": "overwrite",
	"mv":     "overwrite",
	"rimraf": "deletion",
	"chmod":  "permission change", "chown": "permission change", "chgrp": "permission change",
	"chattr": "permission change", "setfacl": "permission change", "usermod": "permission change",
	"passwd": "permission change", "chpasswd": "permission change", "visudo": "permission change",
	"gpasswd": "permission change", "setcap": "permission change",
}

// shells run their arguments (or input) as commands.
var shells = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
	"eval": true, "ssh": true, "autossh": true, "su": true, "watch": true,
	"flock": true, "script": true, "sg": true, "npx": true, "trap": true, "coproc": true}

// sqlClients run SQL given as an argument or on their input.
var sqlClients = map[string]bool{"psql": true, "mysql": true, "mariadb": true, "sqlite3": true, "sqlcmd": true,
	"clickhouse": true, "clickhouse-client": true, "duckdb": true, "cqlsh": true}

// sqlPermission is SQL that changes who may use data, sqlDeletion SQL that deletes it.
var (
	sqlPermission = regexp.MustCompile(`(?i)\b(drop\s+(user|role)|grant\b|revoke\b|alter\s+(user|role))`)
	sqlDeletion   = regexp.MustCompile(`(?i)\b(drop\s+(table|database|schema|index|view)|truncate\b|delete\s+from)`)
)

// onlinePermission is a path in a GitHub API call that is about who may do what.
var onlinePermission = regexp.MustCompile(`(?i)(collaborators|permissions|protection|/teams|/members|memberships|invitations|rulesets|/keys|secrets|/hooks|environments)`)

// personOnly says why cmd must stay with a person ("" when it need not).
func personOnly(cmd string) string { return (&guard{}).personOnly(cmd) }

// guard reads one command for the floor. Its zero value reads it with no scratch targets and nothing let through.
type guard struct {
	allowDestructive bool   // the setting: deletions and overwrites go through (not disk formatting)
	roots            *roots // where scratch targets may be; nil: nowhere
	cwd              string // the session's working directory

	sv  *shellVars // the command string being read
	seg int        // the segment being read in it

	// notes are the deletions and overwrites let through, and why, for the audit log
	notes []string
}

// newGuard is the guard for a session in cwd.
func newGuard(cwd string, allowDestructive bool) *guard {
	return &guard{allowDestructive: allowDestructive, roots: scratchRoots(cwd), cwd: cwd}
}

// personOnly says why cmd must stay with a person ("" when it need not).
func (g *guard) personOnly(cmd string) string {
	g.notes = nil
	return g.personOnlyDepth(cmd, 0)
}

// lets says whether the setting lets a floor reason through: deletion and overwriting, not a forced push, a
// permission change, disk formatting, an online change or an unclear command. It notes the ones it lets through.
func (g *guard) lets(why string) bool {
	if !g.allowDestructive {
		return false
	}
	if (strings.HasPrefix(why, "deletion") && !strings.Contains(why, "permission")) || strings.HasPrefix(why, "overwrite") {
		g.note("setting: " + why)
		return true
	}
	return false
}

func (g *guard) note(n string) {
	for _, x := range g.notes {
		if x == n {
			return
		}
	}
	g.notes = append(g.notes, n)
}

// stays is why, unless the setting lets it through ("").
func (g *guard) stays(why string) string {
	if why == "" || g.lets(why) {
		return ""
	}
	return why
}

func (g *guard) personOnlyDepth(cmd string, depth int) string {
	all, _ := scan(cmd)
	for _, words := range all {
		for _, w := range words {
			if !sqlClients[path.Base(w)] {
				continue
			}
			if sqlPermission.MatchString(cmd) {
				return "permission change (SQL)"
			}
			if sqlDeletion.MatchString(cmd) {
				if why := g.stays("deletion (SQL)"); why != "" {
					return why
				}
			}
		}
	}
	stripped := stripHeredocs(cmd)
	full, redirects, tail := scanFull(stripped)
	sv, seg := g.sv, g.seg
	defer func() { g.sv, g.seg = sv, seg }()
	g.sv = readVars(stripped, full, tail, g.cwd, depth == 0)
	for _, r := range redirects {
		g.seg = r.seg
		if g.scratchTargets(nil, []string{r.target}) {
			g.note("scratch: > " + r.target)
			continue
		}
		if why := g.stays("overwrite: > " + r.target); why != "" {
			return why
		}
	}
	for k, s := range full {
		words := s.words
		g.seg = k
		kept := withoutMessages(words)
		if why := g.segmentPersonOnly(kept, depth); why != "" {
			return why
		}
		if depth >= 3 {
			continue
		}
		shell := false
		for i, w := range kept {
			b := path.Base(w)
			shell = shell || shells[b]
			for _, n := range globMatches(b) {
				shell = shell || shells[n]
			}
			if b == "env" {
				for _, a := range kept[i+1:] {
					if a == "-S" || a == "--split-string" || strings.HasPrefix(a, "-S") || strings.HasPrefix(a, "--split-string=") {
						shell = true
						break
					}
				}
			}
		}
		for _, w := range kept {
			sub := strings.Contains(w, "$(") || strings.Contains(w, "`")
			if sub || (shell && strings.ContainsAny(w, " \t\n;&|()<>")) {
				target := w
				if shell {
					switch {
					case strings.HasPrefix(target, "-S"):
						target = strings.TrimPrefix(target, "-S")
					case strings.HasPrefix(target, "--split-string="):
						target = strings.TrimPrefix(target, "--split-string=")
					case strings.HasPrefix(target, "--command="):
						target = strings.TrimPrefix(target, "--command=")
					case strings.HasPrefix(target, "-c"):
						target = strings.TrimPrefix(target, "-c")
					case strings.HasPrefix(target, "-qc"):
						target = strings.TrimPrefix(target, "-qc")
					}
				}
				if why := g.nested(kept, target, depth+1); why != "" {
					return why
				}
			}
		}
	}
	return ""
}

// nested reads a command string that the words run: with no scratch targets when it runs elsewhere (ssh host '…',
// docker exec c sh -c '…').
func (g *guard) nested(words []string, cmd string, depth int) string {
	for _, w := range words {
		if elsewhere[path.Base(w)] {
			roots := g.roots
			g.roots = nil
			defer func() { g.roots = roots }()
			break
		}
	}
	return g.personOnlyDepth(cmd, depth)
}

// stripHeredocs takes out the body of each heredoc that is not fed to a shell: what cat, tee or python read is data.
// Only a << the shell reads as an operator starts a heredoc: not one inside quotes ('usage: x <<FILE', $'…'),
// backticks, a comment or arithmetic ($((1<<n))), whose following lines are commands. A body starts after the line
// the command's words end on (a quote or a backslash-newline carries them on), and a shell anywhere in those words
// keeps the bodies. Where the scan is unsure it finds no heredoc, so a body is read as commands: that asks more.
func stripHeredocs(cmd string) string {
	lines := strings.Split(cmd, "\n")
	var out []string
	var st heredocScan
	var pending []heredocStart
	var words []string // the lines of the command the pending heredocs are on
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		out = append(out, line)
		words = append(words, line)
		pending = append(pending, st.line(line)...)
		if st.single || st.double || st.cont || st.inside(pending) {
			continue // the command goes on to the next line, and the bodies start after the line it ends on
		}
		keep := false
		segs, _ := scan(strings.Join(words, "\n"))
		for _, ws := range segs {
			for i, w := range ws {
				b := path.Base(w)
				keep = keep || shells[b]
				if b == "env" {
					for _, a := range ws[i+1:] {
						if a == "-S" || a == "--split-string" || strings.HasPrefix(a, "-S") || strings.HasPrefix(a, "--split-string=") {
							keep = true
							break
						}
					}
				}
			}
		}
		for _, h := range pending {
			if h.depth != pending[0].depth {
				// bash reads the bodies of heredocs inside a $( … ) before the ones outside it: leave all of them as
				// commands rather than get the order wrong
				pending = nil
				break
			}
		}
		for _, h := range pending {
			for i+1 < len(lines) {
				i++
				end := lines[i]
				if h.dash {
					end = strings.TrimLeft(end, "\t")
				}
				if end == h.delim {
					out = append(out, lines[i])
					break
				}
				if keep {
					out = append(out, lines[i])
				}
			}
		}
		pending, words = nil, nil
	}
	return strings.Join(out, "\n")
}

// heredocStart is a heredoc operator: its delimiter, whether it was <<- (leading tabs stripped from the body), and
// how many command substitutions it is inside.
type heredocStart struct {
	delim string
	dash  bool
	depth int
}

// heredocScan reads a command line by line for heredoc operators, carrying quotes, backticks and arithmetic across
// lines.
type heredocScan struct {
	single, double bool
	ansi           bool // the single quote is $'…', in which a backslash escapes
	backtick       bool
	cont           bool  // the line ended with a backslash: the command goes on
	arith          int   // open parentheses of a (( … )) or $(( … )), 0 outside one
	subst          []int // for each open $( … ), its open parentheses (its own one included)
}

// depth is how many command substitutions ($( … ) and backticks) the scan is inside.
func (st *heredocScan) depth() int {
	if st.backtick {
		return len(st.subst) + 1
	}
	return len(st.subst)
}

// inside says whether the line ended inside a command substitution that one of the heredocs is outside of: the
// command they are on goes on, and their bodies start after the line it ends on. A heredoc inside $( … ) has its
// body on the next line (x=$(cat <<'EOF' …)), and so has one before a ( … ) subshell that goes on.
func (st *heredocScan) inside(pending []heredocStart) bool {
	for _, h := range pending {
		if st.depth() > h.depth {
			return true
		}
	}
	return false
}

// line is the heredoc operators on one line that the shell reads as operators.
func (st *heredocScan) line(line string) []heredocStart {
	var found []heredocStart
	st.cont = false
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case st.single:
			if st.ansi && r == '\\' {
				i++
			} else if r == '\'' {
				st.single, st.ansi = false, false
			}
		case r == '\\':
			if i == len(rs)-1 {
				st.cont = true
			}
			i++ // the next character is escaped, in double quotes too
		case st.double:
			st.double = r != '"'
		case r == '$' && i+1 < len(rs) && rs[i+1] == '\'':
			st.single, st.ansi = true, true
			i++
		case r == '\'':
			st.single = true
		case r == '"':
			st.double = true
		case r == '`':
			st.backtick = !st.backtick
		case st.arith > 0:
			if r == '(' {
				st.arith++
			} else if r == ')' {
				st.arith--
			}
		case r == '#' && (i == 0 || strings.ContainsRune(" \t;&|()", rs[i-1])):
			return found // a comment, to the end of the line
		case r == '(' && i+1 < len(rs) && rs[i+1] == '(':
			st.arith = 2
			i++
		case r == '$' && i+1 < len(rs) && rs[i+1] == '(' && (i+2 == len(rs) || rs[i+2] != '('):
			st.subst = append(st.subst, 1)
			i++
		case r == '(' && len(st.subst) > 0:
			st.subst[len(st.subst)-1]++
		case r == ')' && len(st.subst) > 0:
			if st.subst[len(st.subst)-1]--; st.subst[len(st.subst)-1] == 0 {
				st.subst = st.subst[:len(st.subst)-1]
			}
		case r == '<' && !st.backtick && i+1 < len(rs) && rs[i+1] == '<':
			if i+2 < len(rs) && rs[i+2] == '<' {
				i += 2 // a here-string
				continue
			}
			var h heredocStart
			i, h = heredocWord(rs, i+1)
			if h.delim != "" {
				h.depth = st.depth()
				found = append(found, h)
			}
		}
	}
	return found
}

// heredocWord reads what follows the << whose second < is at rs[i]: the delimiter is the shell word after it (and
// after a - for <<-), with its quotes and backslashes taken out. It returns the index of the word's last character.
// A word the scan cannot read the way the shell does gives no delimiter: one with $ or a backtick in it ($(echo END)
// is one word to the shell), one that runs into a parenthesis, or one that a backslash-newline carries on.
func heredocWord(rs []rune, i int) (int, heredocStart) {
	var h heredocStart
	if i+1 < len(rs) && rs[i+1] == '-' {
		h.dash = true
		i++
	}
	for i+1 < len(rs) && (rs[i+1] == ' ' || rs[i+1] == '\t') {
		i++
	}
	var w strings.Builder
	var quote rune
	unsure := false
	for i+1 < len(rs) {
		r := rs[i+1]
		if quote == 0 && strings.ContainsRune(" \t;&|()<>", r) {
			unsure = unsure || ((r == '(' || r == ')') && w.Len() > 0)
			break
		}
		i++
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '\'' || r == '"'):
			quote = r
		case r == '\\' && quote != '\'':
			if i+1 == len(rs) {
				unsure = true // a backslash-newline: the word goes on on the next line
				break
			}
			i++
			w.WriteRune(rs[i])
		default:
			unsure = unsure || r == '$' || r == '`'
			w.WriteRune(r)
		}
	}
	if quote != 0 || unsure {
		return i, heredocStart{} // not a delimiter the scan can match
	}
	h.delim = w.String()
	return i, h
}

// withoutMessages drops free text: what follows git's -m and --message, and gh's --title, --body, --notes, -t, -b
// and -n; text that holds a substitution ($(…), `…`) is kept, since the shell runs it.
func withoutMessages(words []string) []string {
	prog := ""
	out := make([]string, 0, len(words))
	for i := 0; i < len(words); i++ {
		w := words[i]
		if b := path.Base(w); b == "git" || b == "gh" {
			prog = b
		}
		flag, val, inline := w, "", false
		if k, v, ok := strings.Cut(w, "="); ok && strings.HasPrefix(k, "--") {
			flag, val, inline = k, v, true
		}
		if !isMessageFlag(prog, flag) {
			out = append(out, w)
			continue
		}
		out = append(out, flag)
		if !inline && i+1 < len(words) {
			i++
			val = words[i]
		}
		if strings.Contains(val, "$(") || strings.Contains(val, "`") {
			out = append(out, val)
		}
	}
	return out
}

func isMessageFlag(prog, flag string) bool {
	switch flag {
	case "-m", "--message":
		return prog == "git"
	case "--title", "--body", "--notes", "-t", "-b", "-n":
		return prog == "gh"
	}
	return false
}

// scan splits cmd into the words of each simple command, as a shell would group them: quotes keep a word together
// (and are removed; $'…' escapes are decoded), a backslash escapes the next character, an unquoted {a,b} or {a..c}
// in a word makes several words, a comment is skipped, and lists, pipes, subshells, groups and command
// substitutions separate. redirects are the files output is written to with >, >|, N> or &> (not >>, not >&N),
// outside /dev/ and /tmp/.
func scan(cmd string) (segs [][]string, redirects []string) {
	full, reds, _ := scanFull(cmd)
	for _, s := range full {
		segs = append(segs, s.words)
	}
	for _, r := range reds {
		redirects = append(redirects, r.target)
	}
	return segs, redirects
}

// segment is one simple command's words, and the separators ( ; \n & | ( ) { } $( and backticks) between the one
// before it and this one: before is "" for the first.
type segment struct {
	words  []string
	before string
}

// redirect is a file output is written to, and the index of the segment it was read in (the one being read then:
// the number of segments finished before it).
type redirect struct {
	target string
	seg    int
}

// scanFull is scan with the separators between segments, and each redirect's segment.
func scanFull(cmd string) (segs []segment, redirects []redirect, tail string) {
	var words []string
	var cur strings.Builder
	var sep strings.Builder // separators since the last segment
	inWord, single, double := false, false, false
	param := 0 // open ${ … } parameter expansions, whose braces are not brace expansion
	endWord := func() {
		if inWord {
			words = append(words, braceExpand(cur.String())...)
			cur.Reset()
			inWord = false
		}
	}
	endSegment := func() {
		endWord()
		if len(words) > 0 {
			segs = append(segs, segment{words: words, before: sep.String()})
			sep.Reset()
			words = nil
		}
	}
	rs := []rune(cmd)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case single:
			if r == '\'' {
				single = false
			} else {
				cur.WriteRune(r)
			}
		case r == '\\' && i+1 < len(rs):
			i++
			if rs[i] != '\n' {
				cur.WriteRune(rs[i])
				inWord = true
			}
		case double:
			if r == '"' {
				double = false
			} else {
				cur.WriteRune(r)
			}
		case r == '#' && !inWord && (i == 0 || strings.ContainsRune(" \t\r\n;&|(<>", rs[i-1])):
			// a comment, to the end of the line: decoding $'…' must not leave a quote in it open. Not after ) or a
			// backtick, which may close a substitution in the word the # is part of ($(x)#y)
			for i+1 < len(rs) && rs[i+1] != '\n' {
				i++
			}
		case r == '$' && i+1 < len(rs) && rs[i+1] == '\'':
			// $'…' is a quote whose backslash escapes the shell decodes: $'\x72m' is rm
			var text string
			i, text = ansiQuote(rs, i+1)
			cur.WriteString(text)
			inWord = true
		case r == '$' && i+1 < len(rs) && rs[i+1] == '"':
			// $"…" is a double quote (translated by the locale): the $ is not part of the word
		case r == '\'':
			single, inWord = true, true
		case r == '"':
			double, inWord = true, true
		case r == '{' || r == '}':
			standalone := !inWord && (i+1 == len(rs) || strings.ContainsRune(" \t\r\n;&|()<>", rs[i+1]))
			switch {
			case r == '{' && i > 0 && rs[i-1] == '$' && inWord:
				param++ // ${name…}
				cur.WriteRune(r)
			case r == '}' && param > 0:
				param--
				cur.WriteRune(r)
			case standalone:
				endSegment() // a { … } group: its words are a command
				sep.WriteRune(r)
			default:
				// part of a word: brace expansion makes it several words ({rm,-rf,x} is rm -rf x)
				cur.WriteRune(map[rune]rune{'{': braceOpen, '}': braceClose}[r])
				inWord = true
			}
		case r == ',' && !single && !double:
			cur.WriteRune(braceComma)
			inWord = true
		case r == '>':
			if inWord && isDigits(cur.String()) {
				cur.Reset()
				inWord = false
			}
			endWord()
			var target string
			if i, target = redirectTarget(rs, i); target != "" {
				redirects = append(redirects, redirect{target: target, seg: len(segs)})
			}
		case r == '<':
			endWord()
		case strings.ContainsRune(";&|\n()`", r):
			endSegment()
			sep.WriteRune(r)
		case r == ' ' || r == '\t' || r == '\r':
			endWord()
		case r == '$' && i+1 < len(rs) && rs[i+1] == '(':
			endSegment()
			sep.WriteRune(r)
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	endSegment()
	return segs, redirects, sep.String()
}

// redirectTarget reads the redirect whose > is at rs[i]: the file it overwrites ("" for >>, >&N, /dev/ and /tmp/),
// and the index of its last character.
func redirectTarget(rs []rune, i int) (int, string) {
	appendTo := false
	if i+1 < len(rs) && rs[i+1] == '>' {
		appendTo = true
		i++
	}
	if i+1 < len(rs) && rs[i+1] == '|' {
		i++
	}
	if i+1 < len(rs) && rs[i+1] == '&' {
		i++
		if i+1 < len(rs) && (rs[i+1] == '-' || (rs[i+1] >= '0' && rs[i+1] <= '9')) {
			return i + 1, ""
		}
	}
	for i+1 < len(rs) && (rs[i+1] == ' ' || rs[i+1] == '\t') {
		i++
	}
	var t strings.Builder
	for i+1 < len(rs) && !strings.ContainsRune(" \t\n;&|()<>", rs[i+1]) {
		i++
		switch {
		case rs[i] == '\\' && i+1 < len(rs):
			// an escaped character is itself (/tmp/\.\./etc), and a backslash-newline joins the next line on
			i++
			if rs[i] != '\n' {
				t.WriteRune(rs[i])
			}
		case rs[i] != '\'' && rs[i] != '"':
			t.WriteRune(rs[i])
		}
	}
	target := t.String()
	if appendTo || target == "" || scratch(target) {
		return i, ""
	}
	return i, target
}

// scratch says whether a file is one nobody keeps: under /dev/ or /tmp/.
// The path is cleaned first, so /tmp/../etc/hosts is not one.
func scratch(f string) bool {
	if !strings.HasPrefix(f, "/") {
		return false
	}
	f = path.Clean(f)
	return strings.HasPrefix(f, "/dev/") || strings.HasPrefix(f, "/tmp/")
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// wrappers run the next program word as a command (sudo rm, xargs rm, find -exec rm, ssh host rm, eval rm,
// busybox rm, autossh host rm, unshare rm, taskset 1 rm, for …; do rm …), so after one every word counts as a
// possible program; shell keywords are here too. ssh runs its unquoted arguments ("ssh host rm -rf x") joined
// with a space through the remote shell, the way it runs one quoted argument; autossh execs ssh; busybox and
// toybox run applets; torsocks, proxychains, firejail and bwrap wrap commands.
var wrappers = map[string]bool{
	"sudo": true, "doas": true, "xargs": true, "parallel": true, "env": true, "nice": true, "ionice": true, "nohup": true,
	"time": true, "timeout": true, "exec": true, "command": true, "builtin": true, "stdbuf": true, "setsid": true,
	"flock": true, "chroot": true, "runuser": true, "strace": true, "unbuffer": true, "watch": true, "ssh": true,
	"autossh": true, "busybox": true, "eval": true,
	"toybox": true, "torsocks": true, "proxychains": true, "proxychains4": true, "firejail": true, "bwrap": true,
	"npx": true, "pnpx": true, "bunx": true, "dlx": true, "uvx": true, "pipx": true,
	"unshare": true, "taskset": true, "coproc": true, "script": true, "sg": true, "trap": true,
	"-exec": true, "-execdir": true, "-ok": true, "-okdir": true,
	"do": true, "then": true, "else": true, "elif": true, "if": true, "while": true, "until": true, "!": true,
}

// segmentPersonOnly looks at one simple command. A word counts as a program when it comes first (after NAME=value
// assignments) or after a wrapper; so `grep -rn truncate src/` and `cd /tmp/rm` go through, `sudo -b rm x` does not.
// A deletion or overwrite whose targets are all scratch (scratch.go) goes through, and so does one the setting lets
// through.
func (g *guard) segmentPersonOnly(words []string, depth int) string {
	return g.segmentFrom(nil, words, depth)
}

// segmentFrom is segmentPersonOnly for words that come after pre in the simple command (a globbed program's).
func (g *guard) segmentFrom(pre, words []string, depth int) string {
	first, wrapped := len(pre) == 0, len(pre) > 0
	for i, w := range words {
		if first && strings.Contains(w, "=") && !strings.HasPrefix(w, "-") {
			continue
		}
		if w == braceTooLarge {
			return "unclear: a brace expansion too large to read"
		}
		program := first || wrapped
		first = false
		name := path.Base(w)
		before := append(append([]string{}, pre...), words[:i]...)
		if program {
			// a globbed program (/bin/r? -rf x) is whichever known program it matches
			for _, n := range globMatches(name) {
				if why := g.segmentFrom(append(before, w), append([]string{n}, words[i+1:]...), depth); why != "" {
					return why
				}
			}
		}
		if name == "command" && program && i+1 < len(words) && (words[i+1] == "-v" || words[i+1] == "-V") {
			return "" // command -v rm only looks rm up
		}
		if wrappers[w] || (program && wrappers[name]) {
			wrapped = true
			continue
		}
		if !program {
			continue
		}
		rest := words[i+1:]
		if why, ok := alwaysPerson[name]; ok {
			r := why + ": " + name
			if name == "dd" && ddDisk(rest) {
				return "disk overwrite: dd"
			}
			if why == "permission change" || diskTools[name] {
				return r
			}
			if g.scratchTargets(before, targetsOf(name, rest)) {
				g.note("scratch: " + name)
				continue
			}
			if r = g.stays(r); r != "" {
				return r
			}
			continue
		}
		if strings.HasPrefix(name, "mkfs") {
			return "disk overwrite: " + name
		}
		var why string
		switch name {
		case "git": // its arguments are not programs (git mv, git rm are git's own)
			return g.stays(g.gitPersonOnly(rest, depth))
		case "gh":
			return g.stays(ghPersonOnly(rest))
		case "find": // what -exec runs is checked as the words after it
			if has(rest, "-delete") {
				why = "deletion: find -delete"
			}
		case "rsync":
			if hasPrefix(rest, "--del") || has(rest, "--remove-source-files") {
				why = "deletion: rsync --delete"
			}
		case "sed", "perl":
			if hasPrefix(rest, "--in-place") || hasShort(rest, 'i') {
				why = "overwrite: " + name + " -i"
			}
		case "ln":
			if has(rest, "--force") || hasShort(rest, 'f') {
				why = "overwrite: ln -f"
			}
		case "install":
			if hasShort(rest, 'm') || hasShort(rest, 'o') || hasShort(rest, 'g') || hasPrefix(rest, "--mode") || hasPrefix(rest, "--owner") || hasPrefix(rest, "--group") {
				return "permission change: install -m"
			}
		case "cp":
			if has(rest, "--force") || has(rest, "--remove-destination") || hasShort(rest, 'f') {
				why = "overwrite: cp -f"
			}
		case "tee":
			if !has(rest, "--append") && !hasShort(rest, 'a') {
				for _, f := range rest {
					if !strings.HasPrefix(f, "-") && !scratch(f) {
						why = "overwrite: tee " + f
						break
					}
				}
			}
		case "docker", "podman":
			if hasAny(rest, "rm", "rmi", "prune") || (has(rest, "down") && (has(rest, "--volumes") || hasShort(rest, 'v'))) {
				why = "deletion: " + name
			}
		case "kubectl", "helm":
			if hasAny(rest, "role", "rolebinding", "clusterrole", "clusterrolebinding", "serviceaccount", "sa") &&
				hasAny(rest, "create", "apply", "edit", "patch", "replace", "set", "delete") {
				return "permission change: " + name
			} else if hasAny(rest, "delete", "uninstall", "drain") {
				why = "deletion: " + name
			}
		case "terraform", "tofu", "pulumi":
			if has(rest, "destroy") || has(rest, "-destroy") {
				why = "deletion: " + name + " destroy"
			}
		case "aws":
			switch {
			case has(rest, "iam") || hasPrefixContaining(rest, "put-", "policy") || hasPrefixContaining(rest, "put-", "acl"):
				return "permission change: aws"
			case hasPrefix(rest, "delete-") || hasPrefix(rest, "terminate-") || has(rest, "--delete") || (has(rest, "s3") && hasAny(rest, "rm", "rb")):
				why = "deletion: aws"
			}
		case "gcloud", "gsutil":
			for _, r := range rest {
				if strings.Contains(r, "iam-policy") || r == "iam" || r == "acl" {
					return "permission change: " + name
				}
			}
			if hasAny(rest, "delete", "rm", "rb") {
				why = "deletion: " + name
			}
		case "az":
			if has(rest, "role") {
				return "permission change: az role"
			} else if has(rest, "delete") {
				why = "deletion: az"
			}
		}
		if why == "" {
			continue
		}
		switch name {
		case "find", "rsync", "ln", "cp", "tee":
			if g.scratchTargets(before, targetsOf(name, rest)) {
				g.note("scratch: " + name)
				continue
			}
		}
		if why = g.stays(why); why != "" {
			return why
		}
	}
	return ""
}

// diskTools format or repartition a disk: they stay with a person even when the setting lets deletions through.
var diskTools = map[string]bool{"wipefs": true, "fdisk": true, "parted": true}

// ddDisk says whether dd writes to a device.
func ddDisk(args []string) bool {
	for _, a := range args {
		if of, ok := strings.CutPrefix(a, "of="); ok && strings.HasPrefix(path.Clean(of), "/dev/") && path.Clean(of) != "/dev/null" {
			return true
		}
	}
	return false
}

// gitPersonOnly looks at what follows git: its subcommand (after -C, -c and other global flags) and that one's
// arguments.
func (g *guard) gitPersonOnly(args []string, depth int) string {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if (args[i] == "-C" || args[i] == "-c" || args[i] == "--git-dir" || args[i] == "--work-tree") && i+1 < len(args) {
			if args[i] == "-c" && depth < 3 {
				if why := g.gitConfigPersonOnly(args[i+1], depth); why != "" {
					return why
				}
			}
			i++
		} else if strings.HasPrefix(args[i], "-c") && depth < 3 {
			if why := g.gitConfigPersonOnly(strings.TrimPrefix(args[i], "-c"), depth); why != "" {
				return why
			}
		}
		i++
	}
	if i >= len(args) {
		return ""
	}
	sub, rest := args[i], args[i+1:]
	switch sub {
	case "bisect":
		for k, a := range rest {
			if a == "run" && k+1 < len(rest) {
				cmdArgs := rest[k+1:]
				for _, w := range cmdArgs {
					if strings.ContainsAny(w, " \t\n;&|()<>") && depth < 3 {
						if why := g.personOnlyDepth(w, depth+1); why != "" {
							return why
						}
					}
				}
				return g.segmentPersonOnly(cmdArgs, depth)
			}
		}
	case "submodule":
		for k, a := range rest {
			if a == "foreach" {
				cmdArgs := rest[k+1:]
				for len(cmdArgs) > 0 && strings.HasPrefix(cmdArgs[0], "-") {
					cmdArgs = cmdArgs[1:]
				}
				for _, w := range cmdArgs {
					if strings.ContainsAny(w, " \t\n;&|()<>") && depth < 3 {
						if why := g.personOnlyDepth(w, depth+1); why != "" {
							return why
						}
					}
				}
				if len(cmdArgs) > 0 {
					return g.segmentPersonOnly(cmdArgs, depth)
				}
			}
		}
	case "rebase":
		for j := 0; j < len(rest); j++ {
			cmd := ""
			if rest[j] == "-x" && j+1 < len(rest) {
				j++
				cmd = rest[j]
			} else if strings.HasPrefix(rest[j], "-x") && len(rest[j]) > 2 {
				cmd = strings.TrimPrefix(rest[j], "-x")
			} else if longOpt(rest[j], "--exec") {
				if _, v, ok := strings.Cut(rest[j], "="); ok {
					cmd = v
				} else if j+1 < len(rest) {
					j++
					cmd = rest[j]
				}
			}
			if cmd != "" && depth < 3 {
				if why := g.personOnlyDepth(cmd, depth+1); why != "" {
					return why
				}
			}
		}
	case "push":
		for _, a := range rest {
			if strings.HasPrefix(a, "--force") || longOpt(a, "--mirror", "--delete", "--prune") ||
				strings.HasPrefix(a, "+") || (strings.HasPrefix(a, ":") && len(a) > 1) || shortFlag(a, 'f') || shortFlag(a, 'd') {
				return "force push: git push " + a
			}
		}
	case "clean", "rm", "filter-branch", "filter-repo":
		return "deletion: git " + sub
	case "restore":
		return "overwrite: git restore"
	case "reset":
		if hasLongOpt(rest, "--hard", "--merge", "--keep") {
			return "overwrite: git reset --hard"
		}
	case "checkout", "switch":
		if has(rest, "--") || has(rest, ".") || hasLongOpt(rest, "--force", "--discard-changes") || hasShort(rest, 'f') ||
			(sub == "checkout" && (hasLongOpt(rest, "--pathspec-from-file") || len(checkoutOperands(rest)) > 1)) {
			return "overwrite: git " + sub
		}
		// -B and -C reset a branch that exists, as git branch -f does; git switch --orphan empties the work tree
		if (sub == "checkout" && hasShort(rest, 'B')) || (sub == "switch" && (hasShort(rest, 'C') || hasLongOpt(rest, "--force-create", "--orphan"))) {
			return "overwrite: git " + sub
		}
	case "branch", "tag":
		if hasLongOpt(rest, "--delete", "--delete-merged", "--force") || hasShort(rest, 'd') || hasShort(rest, 'D') || hasShort(rest, 'f') || hasShort(rest, 'M') {
			return "deletion or overwrite: git " + sub
		}
	case "stash":
		if hasAny(rest, "drop", "clear") {
			return "deletion: git stash drop"
		}
	case "worktree":
		if hasAny(rest, "remove", "prune") {
			return "deletion: git worktree remove"
		}
	case "update-ref":
		if has(rest, "-d") || has(rest, "--stdin") { // --stdin reads delete (and update) lines
			return "deletion: git update-ref -d"
		}
	case "reflog":
		if hasAny(rest, "expire", "delete") {
			return "deletion: git reflog"
		}
	}
	return ""
}

// longOpt says whether a is one of the long options full, or a shortening of one: git takes any unambiguous start
// of a long option (--har for --hard, --dele for --delete), so --x counts for every option that starts with it.
// A value after = is not part of the name.
func longOpt(a string, full ...string) bool {
	name, _, _ := strings.Cut(a, "=")
	if len(name) < 3 || !strings.HasPrefix(name, "--") {
		return false
	}
	for _, f := range full {
		if strings.HasPrefix(f, name) {
			return true
		}
	}
	return false
}

func hasLongOpt(words []string, full ...string) bool {
	for _, w := range words {
		if longOpt(w, full...) {
			return true
		}
	}
	return false
}

// checkoutOperands are git checkout's arguments that are not options or option values: a branch or commit, and
// then the paths to take from it (git checkout HEAD f overwrites f, as git checkout -- f does).
func checkoutOperands(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-b" || a == "-B" || a == "--orphan" || a == "--conflict":
			i++ // the option's value
		case strings.HasPrefix(a, "-"):
		default:
			out = append(out, a)
		}
	}
	return out
}

// ghPersonOnly looks at what follows gh: a permission change counts before a deletion (gh secret delete changes
// who may use the secret), so that the setting does not let it through.
func ghPersonOnly(args []string) string {
	if why := ghPermission(args); why != "" {
		return why
	}
	if has(args, "delete") || has(args, "--delete-branch") || hasPrefix(args, "--delete") {
		return "deletion: gh"
	}
	if ghMethod(args) == "DELETE" {
		return "deletion: gh api DELETE"
	}
	return ""
}

// ghCommand is gh's command and what follows it: gh takes -R/--repo before the command too (gh -R o/r secret set X).
func ghCommand(args []string) []string {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if (args[0] == "-R" || args[0] == "--repo") && len(args) > 1 {
			args = args[1:]
		}
		args = args[1:]
	}
	return args
}

// ghMethod is a gh api call's HTTP method ("" when it is not one).
func ghMethod(args []string) string {
	args = ghCommand(args)
	if len(args) == 0 || args[0] != "api" {
		return ""
	}
	method := "GET"
	for i, a := range args {
		switch {
		case (a == "-X" || a == "--method") && i+1 < len(args):
			method = strings.ToUpper(args[i+1])
		case strings.HasPrefix(a, "-X") && len(a) > 2:
			method = strings.ToUpper(a[2:])
		case strings.HasPrefix(a, "--method="):
			method = strings.ToUpper(strings.TrimPrefix(a, "--method="))
		case a == "-f" || a == "-F" || a == "--field" || a == "--raw-field" || a == "--input" ||
			strings.HasPrefix(a, "--field=") || strings.HasPrefix(a, "--raw-field=") || strings.HasPrefix(a, "--input="):
			if method == "GET" {
				method = "POST"
			}
		}
	}
	return method
}

// ghPermission says why a gh command changes who may do what ("" when it does not).
func ghPermission(all []string) string {
	args := ghCommand(all)
	if len(args) == 0 {
		return ""
	}
	switch args[0] {
	case "secret", "variable", "ssh-key", "gpg-key":
		return "permission change: gh " + args[0]
	case "repo":
		if len(args) > 1 && (args[1] == "deploy-key" || args[1] == "archive" || (args[1] == "edit" && hasPrefix(args, "--visibility"))) {
			return "permission change: gh repo " + args[1]
		}
	case "api":
		method := ghMethod(all)
		if has(args, "graphql") {
			for _, a := range args {
				if strings.Contains(strings.ToLower(a), "mutation") {
					return "online change: gh api graphql mutation"
				}
			}
		}
		if method != "GET" && method != "HEAD" {
			for _, a := range args {
				v := strings.ToLower(a)
				for _, p := range []string{"--field=", "--raw-field=", "-f", "-F"} {
					v = strings.TrimPrefix(v, strings.ToLower(p))
				}
				if strings.HasPrefix(v, "visibility=") || strings.HasPrefix(v, "private=") {
					return "permission change: gh api " + a
				}
			}
			for _, a := range args[1:] {
				if !strings.HasPrefix(a, "-") && onlinePermission.MatchString(a) {
					return "permission change: gh api " + method
				}
			}
		}
	}
	return ""
}

func has(words []string, w string) bool { return hasAny(words, w) }

func hasAny(words []string, ws ...string) bool {
	for _, x := range words {
		for _, w := range ws {
			if x == w {
				return true
			}
		}
	}
	return false
}

// hasPrefixContaining says whether a word starts with p and has c in it (put-bucket-policy).
func hasPrefixContaining(words []string, p, c string) bool {
	for _, x := range words {
		if strings.HasPrefix(x, p) && strings.Contains(x, c) {
			return true
		}
	}
	return false
}

func hasPrefix(words []string, p string) bool {
	for _, x := range words {
		if strings.HasPrefix(x, p) {
			return true
		}
	}
	return false
}

// shortFlag says whether a is a cluster of one-letter flags (-f, -fu) that has c.
func shortFlag(a string, c byte) bool {
	if len(a) < 2 || a[0] != '-' || a[1] == '-' {
		return false
	}
	return strings.IndexByte(a[1:], c) >= 0
}

func hasShort(words []string, c byte) bool {
	for _, x := range words {
		if shortFlag(x, c) {
			return true
		}
	}
	return false
}

// gitConfigPersonOnly looks at a git -c setting. Many values are commands git runs through the shell: a ! alias,
// core.pager, core.editor, core.sshCommand, diff.external, credential and filter helpers and more; so a value that
// starts with ! or holds a space or a shell operator is read as a command, whatever the key.
func (g *guard) gitConfigPersonOnly(cfg string, depth int) string {
	_, val, ok := strings.Cut(cfg, "=")
	if !ok {
		return ""
	}
	if cmd, bang := strings.CutPrefix(val, "!"); bang {
		return g.personOnlyDepth(cmd, depth+1)
	}
	if strings.ContainsAny(val, " \t\n;&|()<>") {
		return g.personOnlyDepth(val, depth+1)
	}
	return ""
}
