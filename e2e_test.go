package main

// End-to-end tests: the real binary runs in a pseudo terminal and its output is
// fed into a terminal emulator, so the tests see the screen a user would see,
// including anything written to stdout/stderr behind the TUI's back.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

const e2eCols, e2eRows = 100, 16

var e2eBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "skillex-e2e-")
	if err != nil {
		panic(err)
	}
	e2eBin = filepath.Join(dir, "skillex")
	if out, err := exec.Command("go", "build", "-o", e2eBin, ".").CombinedOutput(); err != nil {
		panic(fmt.Sprintf("build: %v\n%s", err, out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// e2eEnv is a sandbox with a local source "own", a git source "up" backed by a
// local repository, and one link target.
type e2eEnv struct {
	dir, cfg, target, upstream string
	env                        []string
}

func newE2EEnv(t *testing.T) *e2eEnv {
	t.Helper()
	e := &e2eEnv{dir: t.TempDir()}
	e.cfg = filepath.Join(e.dir, "cfg", "skills.yaml")
	e.target = filepath.Join(e.dir, "target")
	e.upstream = filepath.Join(e.dir, "upstream")
	e.env = append(os.Environ(),
		"HOME="+filepath.Join(e.dir, "home"),
		"XDG_DATA_HOME="+filepath.Join(e.dir, "data"),
		"SKILLEX_CONFIG="+e.cfg,
		"TERM=xterm-256color",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	)
	skill(t, filepath.Join(e.dir, "own", "alpha"), "alpha")
	skill(t, filepath.Join(e.dir, "own", "beta"), "beta")
	skill(t, filepath.Join(e.upstream, "skills", "gamma"), "gamma")
	e.git(t, "init", "-q", "-b", "main")
	e.commit(t, "gamma")
	writeFile(t, e.cfg, fmt.Sprintf(`targets:
  - ../target
sources:
  - name: own
    path: ../own
    enable: all
  - name: up
    git: %s
    ref: main
    enabled:
      - skills/gamma
`, e.upstream))
	// Clone before the TUI starts, like a normal first `skillex sync`.
	cmd := exec.Command(e2eBin, "sync")
	cmd.Env = e.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	return e
}

func (e *e2eEnv) git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = e.upstream, e.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (e *e2eEnv) commit(t *testing.T, msg string) {
	e.git(t, "add", "-A")
	e.git(t, "commit", "-q", "-m", msg)
}

type tui struct {
	t    *testing.T
	term vt10x.Terminal
	pty  *os.File
	cmd  *exec.Cmd
	done chan struct{}
}

func startTUI(t *testing.T, e *e2eEnv) *tui {
	t.Helper()
	cmd := exec.Command(e2eBin)
	cmd.Env = e.env
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: e2eCols, Rows: e2eRows})
	if err != nil {
		t.Fatal(err)
	}
	u := &tui{t: t, pty: f, cmd: cmd, done: make(chan struct{}),
		term: vt10x.New(vt10x.WithWriter(f), vt10x.WithSize(e2eCols, e2eRows))}
	go func() {
		defer close(u.done)
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				u.term.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		f.Close()
		<-u.done
	})
	u.waitFor("first frame", func(s []string) bool { return strings.HasPrefix(s[0], "skillex") })
	return u
}

func (u *tui) screen() []string {
	lines := strings.Split(strings.TrimSuffix(u.term.String(), "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

func (u *tui) dump() string {
	return "\n┌" + strings.Repeat("─", e2eCols) + "\n│" + strings.Join(u.screen(), "\n│") + "\n└"
}

func (u *tui) waitFor(desc string, cond func([]string) bool) {
	u.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond(u.screen()) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	u.t.Fatalf("timed out waiting for %s; screen:%s", desc, u.dump())
}

func (u *tui) waitText(text string) {
	u.t.Helper()
	u.waitFor(fmt.Sprintf("%q", text), func(s []string) bool { return strings.Contains(strings.Join(s, "\n"), text) })
}

var keyBytes = map[string]string{
	"space": " ", "enter": "\r", "esc": "\x1b", "tab": "\t",
	"up": "\x1b[A", "down": "\x1b[B",
}

func (u *tui) press(keys ...string) {
	u.t.Helper()
	for _, k := range keys {
		b, ok := keyBytes[k]
		if !ok {
			b = k
		}
		if _, err := u.pty.WriteString(b); err != nil {
			u.t.Fatal(err)
		}
		// Let the program handle each key separately (esc sequences would merge).
		time.Sleep(60 * time.Millisecond)
	}
}

// assertLayout checks the fixed frame: title, blank line, list rows, status, help.
func (u *tui) assertLayout() {
	u.t.Helper()
	s := u.screen()
	bad := func(why string) { u.t.Fatalf("broken layout: %s; screen:%s", why, u.dump()) }
	if len(s) != e2eRows {
		bad(fmt.Sprintf("%d lines", len(s)))
	}
	if !strings.HasPrefix(s[0], "skillex") {
		bad("title not on first line")
	}
	if s[1] != "" {
		bad("second line not blank")
	}
	if !strings.HasPrefix(s[len(s)-1], "space toggle") {
		bad("help not on last line")
	}
	for _, l := range s[2 : len(s)-2] {
		if l != "" && !strings.HasPrefix(l, "▌ ") && !strings.HasPrefix(l, "  [") {
			bad(fmt.Sprintf("unexpected list line %q", l))
		}
	}
}

func findRow(s []string, prefix string) string {
	for _, l := range s {
		if strings.HasPrefix(strings.TrimSpace(l), prefix) {
			return l
		}
	}
	return ""
}

func TestE2EToggle(t *testing.T) {
	e := newE2EEnv(t)
	u := startTUI(t, e)
	u.assertLayout()

	u.press("down", "space") // own/alpha
	u.waitText("disabled alpha")
	u.assertLayout()
	if !strings.HasPrefix(findRow(u.screen(), "[ ] alpha"), "  [ ] alpha") {
		t.Fatalf("alpha not unchecked:%s", u.dump())
	}
	if data, _ := os.ReadFile(e.cfg); !strings.Contains(string(data), "disabled:\n      - alpha") {
		t.Fatalf("config not saved:\n%s", data)
	}
	if _, err := os.Lstat(filepath.Join(e.target, "alpha")); err == nil {
		t.Fatal("alpha still linked")
	}

	u.press("space")
	u.waitText("enabled alpha")
	if _, err := os.Lstat(filepath.Join(e.target, "alpha")); err != nil {
		t.Fatal("alpha not relinked")
	}
}

func TestE2EUpdateKeepsLayout(t *testing.T) {
	e := newE2EEnv(t)
	skill(t, filepath.Join(e.upstream, "skills", "delta"), "delta")
	e.commit(t, "delta")

	u := startTUI(t, e)
	u.press("tab") // source header "up"
	u.press("u")
	u.waitText("new: skills/delta")
	u.assertLayout()
	u.waitText("[ ] delta")

	// The UI must stay usable after the update.
	u.press("down", "space") // delta sorts before gamma
	u.waitText("enabled delta")
	u.assertLayout()
	if _, err := os.Lstat(filepath.Join(e.target, "delta")); err != nil {
		t.Fatal("delta not linked after update")
	}
}

func TestE2ERemoveSource(t *testing.T) {
	e := newE2EEnv(t)
	u := startTUI(t, e)
	u.press("tab", "d")
	u.waitText("Remove source up?")
	u.press("n")
	u.waitText("kept up")
	u.assertLayout()

	u.press("d", "y")
	u.waitText("removed up")
	u.assertLayout()
	if findRow(u.screen(), "▌ up") != "" {
		t.Fatalf("source still shown:%s", u.dump())
	}
	if _, err := os.Lstat(filepath.Join(e.target, "gamma")); err == nil {
		t.Fatal("gamma still linked")
	}
}

func TestE2EFilter(t *testing.T) {
	e := newE2EEnv(t)
	u := startTUI(t, e)
	u.press("/", "g", "a", "m")
	u.waitText("/gam")
	u.waitFor("only gamma listed", func(s []string) bool {
		return findRow(s, "[x] gamma") != "" && findRow(s, "[x] alpha") == "" && findRow(s, "▌ own") == ""
	})
	u.press("enter", "space")
	u.waitText("disabled gamma")
	u.assertLayout()
}

func TestSyncFailsOnConflict(t *testing.T) {
	e := newE2EEnv(t)
	link := filepath.Join(e.target, "alpha")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(link, "SKILL.md"), "local\n")
	cmd := exec.Command(e2eBin, "sync")
	cmd.Env = e.env
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "alpha exists and is not managed") {
		t.Fatalf("expected conflict failure, got %v:\n%s", err, out)
	}
	if data, _ := os.ReadFile(filepath.Join(link, "SKILL.md")); string(data) != "local\n" {
		t.Fatal("local skill overwritten")
	}
}
