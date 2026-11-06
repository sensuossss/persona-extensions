package main

import (
	"strings"
	"testing"
	"time"
)

func TestPersonOnly(t *testing.T) {
	stays := []string{
		// deletion
		"rm -f a/*", "cd view && rm -rf \"$t\"", "/bin/rm x", "'rm' -r d", "for d in *; do rmdir $d; done", "unlink f",
		"shred -u key", "find . -name '*.tmp' -delete", "git clean -fdx", "git rm -r --cached x", "git branch -D topic",
		"git tag -d v1", "git stash drop", "git stash clear", "git worktree remove ../wt", "rsync -a --delete src/ dst/",
		"docker rm -f c1", "docker system prune -a", "kubectl delete pod p", "gh repo delete me/r --yes",
		"gh pr merge 3 --squash --delete-branch", "gh api -X DELETE repos/o/r/git/refs/heads/x",
		`psql -c "DROP TABLE users"`, `sqlite3 db 'delete from t'`, "x=$(rm -rf y)", "echo a | xargs rm", "sudo rm -rf /var/x",
		// overwriting data
		"dd if=/dev/zero of=/dev/sda", "mkfs.ext4 /dev/sdb1", "truncate -s 0 log", "git reset --hard origin/main",
		"git checkout -- src/a.go", "git checkout -f main", "git checkout .", "git restore src/", "git switch --discard-changes x",
		// forced push
		"git push --force", "git push -f origin main", "git push --force-with-lease", "git -C repo push origin +main",
		"git push origin :old", "git push --delete origin x", "git push --mirror", "cd r && git  push  -f",
		// permission changes, local and online
		"chmod 777 x", "chown me f", "sudo usermod -aG docker me", "setfacl -m u:x:r f", "passwd",
		"gh secret set TOKEN < t", "gh repo edit --visibility public", "gh api -X PUT repos/o/r/collaborators/u",
		"gh api repos/o/r/branches/main/protection -X DELETE", "gh api repos/o/r/collaborators/u -f permission=admin",
		"gh ssh-key add k.pub", "aws iam attach-user-policy --user-name u", "gcloud projects add-iam-policy-binding p",
		"kubectl create rolebinding rb --role=r", "az role assignment create",
		// from the review: online visibility, global flags, cloud and SQL deletions, overwrites, nested commands
		"gh api -X PATCH repos/o/r -f visibility=public", "gh api graphql -f query='mutation{updateRepository(x)}'",
		"aws --profile prod iam attach-role-policy --role-name r", "az --subscription s role assignment create",
		"aws s3api put-bucket-policy --bucket b", "psql -c 'TRUNCATE users'", "psql -c 'GRANT ALL ON t TO u'",
		"aws s3 rm s3://b/x --recursive", "aws s3 rb s3://b --force", "aws dynamodb delete-table --table-name t",
		"gcloud compute instances delete i", "az group delete -n g", "terraform destroy", "docker compose down -v",
		": > data.db", "echo {} > settings.json", "mv build/out.db data/prod.db", "cp -f a b", "tee config.json",
		"bash -c 'rm -rf x'", "sh -c \"git push --force\"", "find . -name x -exec rm {} +", "git branch -M main",
		// from the second review
		`git commit -m "$(rm -rf ~/data)"`, "git commit -m \"`rm -rf x`\"", "find . | xargs -t rm -f", "sudo -b rm -rf /var/lib/app",
		"ls &> data.db", "ls 1> data.db", "aws s3 sync ./out s3://b --delete", "gh api -X PATCH repos/o/r --field=visibility=public",
		"ssh host 'rm -rf /srv/x'", "bash <<EOF\nrm -rf x\nEOF", "psql <<SQL\nDROP TABLE t;\nSQL",
		// third review
		"rsync -a --del src/ dst/", "npx rimraf dist", "sed -i 's/a/b/' config.yaml", "sed --in-place=.bak s/a/b/ f",
		"ln -sf /dev/null ~/.bash_history", "install -m 600 key ~/.ssh/id",
		// a << the shell does not read as a heredoc hides nothing after it: in quotes, a comment or arithmetic
		"echo 'usage: x <<FILE'\ncd view && rm -f sub/*", "echo \"a <<EOF\"\nrm -rf \"$t\"\nEOF",
		"git commit -m 'msg\nsee <<END'\nrm -rf \"$t\"", "# feed it <<EOF\nrm -rf \"$t\"\nEOF",
		"n=$((1<<shift))\nrm -rf \"$t\"", "(( m = 1<<k ))\nrm -rf \"$t\"", "cat <<<'x <<EOF'\nrm -rf \"$t\"",
		// two heredocs on a line: both bodies are skipped, then the commands after them count
		"cat <<A <<B\nx\nA\ny\nB\nrm -rf \"$t\"",
		// ssh runs its unquoted arguments, joined with a space, through the remote shell
		"ssh host rm -rf /srv/x", "ssh -p 2222 user@host rm -rf /srv/x", "ssh host truncate -s 0 /data/db",
		"ssh host chmod 777 /etc/passwd", "ssh host git push --force", "ssh host docker rm -f c1",
		"/usr/bin/ssh host rm -rf /x", "sudo ssh host rm -rf /x", "ssh host ssh host2 rm -rf /y",
		// after ssh every word is a possible program: a path whose name is a floor program errs toward asking
		"ssh host ls /tmp/rm",
		// other command wrappers: eval runs its words in this shell, autossh execs ssh, busybox runs its applet
		"eval rm -rf /x", "eval git push --force", "autossh -M 0 host rm -rf /x",
		"busybox rm -rf /x", "busybox sh -c 'rm -rf /x'",
		// command runners and wrappers: env -S/--split-string, flock/script/sg/npx -c, trap, coproc, unshare, taskset
		"env -S 'rm -rf v'", "env --split-string='rm -rf v'", `env -S"rm -rf v"`,
		"flock /tmp/l -c 'rm -rf v'", "script -qc 'rm -rf v' /dev/null", "script -c 'rm -rf v'",
		"sg grp -c 'rm -rf v'", "npx -c 'rm -rf v'",
		"trap 'rm -rf v' EXIT",
		"coproc rm -rf v", "coproc NAME { rm -rf v; }",
		"unshare rm -rf v", "taskset 1 rm -rf v",
		// other shells (csh and tcsh ship in macOS's /bin), and sudo -s/-i or doas -s, which start one that reads its input
		`csh -c 'rm -rf "$d"'`, `tcsh -c 'rm -rf "$d"'`, "mksh -c 'rm -rf v'", "busybox ash -c 'rm -rf v'", "yash -c 'rm -rf v'",
		"pwsh -c 'rm -rf v'", "tcsh <<EOF\nrm -rf \"$d\"\nEOF", "sudo -s <<EOF\nrm -rf \"$d\"\nEOF",
		"sudo -i <<'EOF'\nrm -rf /var/x/*\nEOF", "sudo -u root -s <<EOF\nrm -rf v\nEOF", "sudo -Hi <<EOF\nrm -rf v\nEOF",
		"sudo --login <<EOF\nrm -rf v\nEOF", "doas -s <<EOF\nrm -rf v\nEOF",
		"sudo --user root -s <<EOF\nrm -rf v\nEOF", "sudo --group staff -i <<EOF\nrm -rf v\nEOF",
		// git command runners: rebase -x/--exec, bisect run, submodule foreach, -c alias.!
		"git rebase -x 'rm -rf v' HEAD~1", "git rebase --exec='rm -rf v'", "git rebase --exec 'rm -rf v'",
		"git rebase --exe='rm -rf v'", "git rebase --exe 'rm -rf v'",
		"git -c alias.x='!rm -rf v' x",
		"git -c core.sshCommand='rm -rf v' fetch", "git -c diff.external='rm -rf v' diff", "git -c core.pager='rm -rf v' log",
		`git -c core.editor="sh -c 'rm -rf v'" commit --amend`, "git -c credential.helper='!rm -rf v' fetch",
		"git bisect run rm -rf v",
		"git submodule foreach 'rm -rf v'", "git submodule foreach --recursive 'rm -rf v'",
		// the environment variables git runs through the shell like those -c values, before git or after env
		"GIT_SSH_COMMAND='rm -rf v' git fetch", "GIT_EDITOR='rm -rf v' git commit --amend",
		"GIT_SEQUENCE_EDITOR='rm -rf v' git rebase -i HEAD~2", "GIT_EXTERNAL_DIFF='rm -rf v' git diff",
		"GIT_PAGER='rm -rf v' git -p log", "PAGER='rm -rf v' git log", "EDITOR='rm -rf v' git commit",
		"FOO=1 VISUAL='rm -rf v' git commit", "env GIT_SSH_COMMAND='rm -rf v' git fetch",
		"sudo env GIT_EDITOR='rm -rf v' git commit", "env -u X GIT_PAGER='rm -rf v' git log",
		// a /tmp/ or /dev/ path that .. takes out of it is a kept file
		"echo x > /tmp/../etc/hosts", "go test > /tmp/a/../../home/u/.bashrc 2>&1", "tee /tmp/../home/u/.profile",
		"ls &> /dev/../etc/motd", "echo x > /tmp/\\.\\./etc/hosts", "echo x > /tmp/\\\n../etc/hosts",
		// gh's -R/--repo before the command
		"gh -R o/r secret set TOKEN", "gh --repo o/r variable set X", "gh --repo=o/r secret delete-all", "gh -Ro/r ssh-key add k.pub",
		"gh -R o/r repo deploy-key add k.pub", "gh -R o/r repo edit --visibility public",
		// from the review: $'…' escapes, nested arithmetic, a continued line, a shell on the line before, backticks,
		// and delimiters with - or # in them
		"cat <<X $'a\\''\ndata\nX\n# '\nrm -rf \"$t\"\nX", "echo $(( (1+(2)) << x ))\nrm -rf \"$t\"",
		"cat <<EOF \\\n; rm -rf \"$t\"\nEOF", "bash \\\n<<EOF\nrm -rf \"$t\"\nEOF", "echo `cat <<'E'`\nrm -rf \"$t\"\nE",
		"cat <<E-O-F\nx\nE-O-F\nrm -rf \"$t\"\nE", "cat <<EOF#c\nx\nEOF#c\nrm -rf \"$t\"\nEOF",
		"cat <<$(echo END)\ndata\n$(echo END)\nrm -rf \"$t\"\n$", "cat <<EOF\\\nFOO\nx\nEOFFOO\nrm -rf \"$t\"\nEOF\\",
		"cat <<A `echo\ny`; rm -rf \"$t\"\nA", "cat <<A $(echo\ny); rm -rf \"$t\"\nA",
		"cat <<A; x=$(cat <<B\nbbody\nB\n)\nabody\nA\nrm -rf \"$t\"",
		// git takes any unambiguous start of a long option, and git checkout <commit> <path> overwrites the path
		"git push --dele origin x", "git push --mirr", "git push --prun origin 'refs/heads/*:refs/heads/*'",
		"git reset --har", "git reset --ha HEAD~1", "git reset --kee HEAD~1", "git checkout --forc main",
		"git switch --discard main", "git branch --del x", "git branch --delete-merged 'origin/*'", "git tag --forc v1",
		"git checkout HEAD f", "git checkout origin/main -q src/a.go",
		"git checkout --pathspec-from-file=list main", "git checkout --pathspec-fr=list main", "git checkout main --pathspec-fr list",
		"git checkout -B feat", "git checkout -B x origin/x", "git switch -C feat", "git switch --force-create feat main",
		"git switch --orphan new", "printf 'delete refs/heads/keep\\n' | git update-ref --stdin",
		// the shell's expansions spell the program another way: $'…' and $"…" quotes, brace expansion, a glob
		"$'rm' -rf v", `$"rm" -rf v`, `$'\x72m' -rf v`, `$'\162m' -rf v`, `$'\u0072m' -rf v`, "$'r'm -rf v",
		"{rm,-rf,v}", "{rm,} -rf v", "r{m,} -rf v", "{r..r}m -rf v", "sudo {rm,-rf,v}", "git push origin {+main,dev}",
		"/bin/r? -rf v", "/bin/r[m] -rf v", "/bin/r[!x] -rf v", "/usr/bin/r[l-n] -rf v", "r* -rf v",
		"/usr/bin/su?o rm -rf v", "ba?h -c 'rm -rf v'", "bash -c $'rm -rf v'", "cd x && /bin/r? -rf v",
		// from the review: a word expansion leaves empty is dropped, and a # in a word after $(…) is not a comment
		"{,rm} -rf v", "{,} rm -rf v", "{,rm,-rf,v}", "{,rm}{,} -rf v", "{,/usr/bin/rm} -rf v", "{{rm,-rf},x}",
		"{" + strings.Repeat(",", 5000) + "rm} -rf v", "echo " + strings.Repeat("{a,b}", 20) + "; rm -rf v",
		"echo $(true)#y; rm -rf v", "echo `true`#y; rm -rf v", "echo $(x)#c && rm -rf v", "x$(echo hi)#z; rm -rf v",
	}
	for _, c := range stays {
		if personOnly(c) == "" {
			t.Errorf("%q should stay with a person", c)
		}
	}
	goes := []string{
		"ls -la", "echo x > /tmp/a/../b.log", "tee /tmp/./x.log", "ls > //tmp/x", "echo x > /tmp/a\\ b.log", "gh --repo o/r issue view 3", "cd view && ls sub/*", "go test ./...", "git status", "git push", "git push origin feat/x",
		"git push -u origin feat/x", "git checkout -b feat/x", "git checkout main", "git switch feat/x", "git branch -a",
		"git reset HEAD~1", "git stash list", "git stash push -m x", "git fetch --prune", "git log --format=%H",
		"gh pr create --title t", "gh pr merge 3 --squash", "gh api repos/o/r/pulls", "gh api repos/o/r/collaborators",
		"gh issue create -t x -b y", "make build", "mkdir -p a/b", "cp a b", "rsync -a src/ dst/",
		"python3 -c 'print(1)'", "curl -s https://example.com", "docker ps", "kubectl get pods", "npm run format",
		"grep -rn armed .", "cat firmware.bin | head", "echo hello >> out.txt", "make 2>&1 | tail", "ls > /dev/null",
		"git commit -m 'drop rm wrapper'", "git commit -m 'Fix dd parsing'", "gh pr create --title x --body 'We delete the old cache'",
		"cd /tmp/rm && ls", "kubectl get rolebinding -A", "aws s3 ls", "terraform plan", "docker compose down",
		"bash -c 'go test ./...'", "find . -name '*.go'",
		"git commit -m 'Map a -> b'", "python3 -c 'print(1 if 2 > 1 else 0)'", "awk '$3 > 100' data.csv", "grep -n '<div>' index.html",
		"cat docs/grant.md", "grep -rn truncate src/", "git mv old new", "find . -name '*.go' -exec grep -l x {} +",
		"go build | tee /tmp/build.log", "go test > /tmp/test.log 2>&1", "cat > /tmp/x.json <<EOF\n{\"rm\": 1}\nEOF",
		"cat <<'EOF' | python3 -\nimport os; print('rm -rf would be bad')\nEOF", "cat <<A <<'B'\nrm a\nA\nrm b\nB",
		"diff <(cat <<EOF\nrm x\nEOF\n) f", "cat > /tmp/x <<-EOF\n\trm x\n\tEOF", "echo \"$((1<<3))\" && cat <<EOF\nrm x\nEOF",
		"((cd /tmp) && echo ok)\ncat > /tmp/o <<'EOF'\nrm x\nEOF", "cat > /tmp/o <<\\EOF\nrm x\nEOF",
		"git commit -m \"$(cat <<'EOF'\nDrop the rm -rf cleanup\nEOF\n)\"", "python3 - <<'PY'\nimport os; os.remove('x')\nPY",
		"x=$(cat <<'EOF'\nrm x\nEOF\n)", "(cat <<EOF\nrm x\nEOF\n) | wc -l", "cat <<EOF | (wc -l\nrm x\nEOF\n)", "git log --format='%h > %s'", "echo ok 2>/dev/null",
		"command -v rm", "sed -n 1,20p f", "sed 's/a/b/' f", "ln -s a b", "npx prettier --check .",
		// a read-only remote command, or a plain login, still goes: the ssh fix only asks toward destruction
		"ssh host ls -la", "ssh host git status", "ssh host kubectl get pods", "ssh -p 2222 user@host docker ps",
		"ssh user@host", "ssh host git push origin feat/x", "ssh host kubectl apply -f app.yaml",
		"ssh -L 8080:localhost:80 -N host", "ssh host 'echo chmod 755 in text'",
		// benign words after the other wrappers still go through (eval "$(ssh-agent)" is a common shell init line)
		"eval echo hi", "busybox ls -la", "busybox true", "autossh -M 0 host", `eval "$(ssh-agent -s)"`,
		// git options that are not shortenings of a destructive one, and a checkout of one branch or commit
		"git push --dry-run origin x", "git push --porcelain", "git push --progress origin x", "git reset --soft HEAD~1",
		"git checkout -b feat/x origin/main", "git checkout --orphan new main",
		"git checkout --track origin/x", "git checkout -t origin/x", "git checkout -q main", "git checkout --detach v1",
		"git switch -c x origin/main", "git branch --list 'feat/*'", "git tag --list", "git checkout --conflict diff3 main",
		// braces, globs and $'…' that do not make a floor program, and a comment
		"cat {a,b}.txt", "ls src/*.go", "echo ${HOME}", "echo ${x,,}", "mkdir -p dir/{a,b}", "git log --format='{%h}'",
		"echo $'hello\n'", "[ -f x ] && echo y", "find . -exec grep x {} \\;", "{ echo a; echo b; } | wc -l",
		"echo '{rm,x}'", "jq '.a | {b,c}' f.json", "cp -r src/{a,b} dst/", "echo x # rm -rf y", "ls *", "./*.sh",
		"echo {1..3}", "printf '%s\n' {a..e}", "go test ./... -run 'Test{A,B}'",
		"for i in {1..10000}; do echo $i; done", "seq 1 {1..5000}",
		"echo {,a}", "ls *.{go,md}", "awk -F, '{print $1}' f.csv", "echo a#b", "echo x #c; # rm -rf v", "(cd x) # rm -rf v",
		// benign forms for command runners and git subcommands still go through
		"env FOO=1 go test ./...", "flock /tmp/l make", "trap - EXIT", "trap 'echo done' EXIT",
		"git rebase -i HEAD~3", "git rebase --exec 'go test ./...' HEAD~2", "git bisect run go test ./...",
		"git submodule foreach git pull", "git -c alias.st=status st", "script -q /dev/null",
		"git -c user.name='A B' commit -m x", "git -c core.pager='less -R' log", "git -c core.sshCommand='ssh -i k' fetch",
		"GIT_SSH_COMMAND='ssh -i k' git fetch", "GIT_EDITOR=vim git commit", "env GIT_PAGER='less -R' git log",
		// a heredoc fed to sudo without its shell options is data, and a benign command in another shell still goes
		"sudo -u me cat <<EOF\nrm x\nEOF", "sudo -g staff -E cat <<EOF\nrm x\nEOF", "csh -c 'ls -la'", "tcsh <<EOF\necho hi\nEOF",
	}
	for _, c := range goes {
		if why := personOnly(c); why != "" {
			t.Errorf("%q: %s, should go through", c, why)
		}
	}
}

func TestHookOtherTools(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	for _, c := range []struct {
		tool  string
		input map[string]any
	}{
		{"Read", map[string]any{"file_path": "/etc/hosts"}},
		{"Glob", map[string]any{"pattern": "**/*.go", "path": "/srv"}},
		{"Grep", map[string]any{"pattern": "TODO", "path": "/srv"}},
		{"LS", map[string]any{"path": "/srv"}},
		{"NotebookRead", map[string]any{"notebook_path": "/srv/a.ipynb"}},
		{"Agent", map[string]any{"description": "look around", "prompt": "…", "subagent_type": "Explore"}},
		{"Task", map[string]any{"description": "look around", "prompt": "…"}},
	} {
		req := request(e, "bypassPermissions", c.tool, "")
		req["tool_input"] = c.input
		if got := e.hook(t, req, now); strings.TrimSpace(got) != allowOutput {
			t.Errorf("%s in bypass mode: %q", c.tool, got)
		}
		req["permission_mode"] = "auto"
		if got := e.hook(t, req, now); got != "" {
			t.Errorf("%s in auto mode: %q", c.tool, got)
		}
	}
	got := readAudit(e.data, 1)
	if len(got) != 1 || got[0].Tool != "Task" || got[0].Command != "Task look around" {
		t.Errorf("audit of a non-Bash call: %+v", got)
	}
	read := readAudit(e.data, 7)[6]
	if read.Tool != "Read" || read.Command != "Read /etc/hosts" {
		t.Errorf("audit of Read: %+v", read)
	}
}

func TestHookLeavesDestructiveBash(t *testing.T) {
	b := newBox(t)
	e := newEnv(t)
	for _, cmd := range []string{"cd view && rm -f sub/*", "git push --force", "chmod -R 777 .", "git reset --hard", "ssh host rm -rf /srv/x"} {
		req := request(e, "bypassPermissions", "Bash", cmd)
		req["cwd"] = b.home // not a project: relative targets are not scratch
		if got := e.hook(t, req, time.Now()); got != "" {
			t.Errorf("%q: answered %q, should stay with a person", cmd, got)
		}
	}
	if n := len(readAudit(e.data, 10)); n != 0 {
		t.Errorf("%d audit entries for refusals", n)
	}
}

func TestHookLeavesRulesForOtherTools(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	writeJSON(t, e.config+"/settings.json", `{"permissions":{"ask":["Read(./secrets/**)"],"deny":["Task(Explore)"]}}`)
	read := request(e, "bypassPermissions", "Read", "")
	read["tool_input"] = map[string]any{"file_path": "/srv/notes.md"}
	if got := e.hook(t, read, now); got != "" {
		t.Errorf("an ask rule for Read: every Read stays with Claude Code, got %q", got)
	}
	agent := request(e, "bypassPermissions", "Agent", "")
	agent["tool_input"] = map[string]any{"description": "x", "prompt": "y"}
	if got := e.hook(t, agent, now); got != "" {
		t.Errorf("a deny rule for Task covers Agent: %q", got)
	}
	grep := request(e, "bypassPermissions", "Grep", "")
	grep["tool_input"] = map[string]any{"pattern": "x"}
	if got := e.hook(t, grep, now); got != "" {
		t.Errorf("a Read rule covers Grep too: %q", got)
	}
	free := newEnv(t)
	grep["cwd"] = free.project
	if got := free.hook(t, grep, now); got == "" {
		t.Error("no rule is about Grep: should be allowed")
	}
	if got := e.hook(t, request(e, "bypassPermissions", "Bash", "ls"), now); got == "" {
		t.Error("no rule is about Bash: should be allowed")
	}
}

func TestMatcher(t *testing.T) {
	if hookMatcher != "Bash|Read|Glob|Grep|LS|NotebookRead|Agent|Task" {
		t.Errorf("matcher %q", hookMatcher)
	}
	for _, r := range []struct {
		rule, tool string
		match      bool
	}{
		{"Read", "Read", true}, {"Read(~/x)", "Read", true}, {"Task", "Agent", true}, {"Agent(Explore)", "Task", true},
		{"Read(~/.ssh/**)", "Grep", true}, {"Read(~/.ssh/**)", "Glob", true}, {"Read", "LS", true},
		{"Read", "NotebookRead", true}, {"Grep", "Read", false},
		{"Write(./x)", "Read", false}, {"Bash(git push:*)", "Read", false}, {"mcp__x", "Agent", false},
	} {
		got := matchingRule([]string{r.rule}, r.tool, "") != ""
		if got != r.match {
			t.Errorf("%s for %s: %v", r.rule, r.tool, got)
		}
	}
}

// TestBraceExpandBounded: a long or deeply nested brace word is read in bounded time, and its first words still are.
func TestBraceExpandBounded(t *testing.T) {
	for _, c := range []string{
		strings.Repeat("{", 50000) + strings.Repeat("}", 50000),
		strings.Repeat("A", 50000) + "{1..4096}",
		strings.Repeat("{a,b}", 30),
		"{rm,x} -rf " + strings.Repeat("{a,b}", 30),
	} {
		start := time.Now()
		why := personOnly(c)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%.20q…: %v", c, d)
		}
		if strings.HasPrefix(c, "{rm") && why == "" {
			t.Errorf("%.20q… should stay with a person", c)
		}
	}
}
