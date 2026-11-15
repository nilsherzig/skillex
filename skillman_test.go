package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skill(t *testing.T, dir, name string) {
	writeFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: "+name+"\ndescription: >\n  does\n  "+name+"\n---\nbody\n")
}

// setup creates two local sources and a config with one target.
func setup(t *testing.T) (*App, string, string) {
	t.Helper()
	// Real path: the config path is resolved, and on macOS the temp
	// directory lies behind the /tmp -> /private/tmp symlink.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	skill(t, filepath.Join(dir, "own", "alpha"), "alpha")
	skill(t, filepath.Join(dir, "own", "beta"), "beta")
	skill(t, filepath.Join(dir, "own", "beta", "nested"), "nested") // inside a skill: ignored
	skill(t, filepath.Join(dir, "own", ".hidden", "h"), "h")
	skill(t, filepath.Join(dir, "up", "group", "gamma"), "gamma")
	skill(t, filepath.Join(dir, "up", "delta"), "alpha") // same name as own/alpha
	cfg := filepath.Join(dir, "cfg", "skills.yaml")
	writeFile(t, cfg, `# top comment
targets:
  - ../target # relative
sources:
  # mine
  - name: own
    path: ../own
    enable: all
  - name: up
    path: ../up
    enabled:
      - group/gamma # keep me
`)
	app := NewApp(cfg, nil)
	if err := app.Load(nil); err != nil {
		t.Fatal(err)
	}
	return app, dir, cfg
}

func links(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		dest, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			dest = "<not a link>"
		}
		out[e.Name()] = dest
	}
	return out
}

func TestScan(t *testing.T) {
	app, _, _ := setup(t)
	var ids []string
	for _, sk := range app.Find("own").Skills {
		ids = append(ids, sk.ID+"="+sk.Name+":"+sk.Description)
	}
	if got := strings.Join(ids, ","); got != "alpha=alpha:does alpha,beta=beta:does beta" {
		t.Fatalf("own skills: %s", got)
	}
	if n := len(app.Find("up").Skills); n != 2 {
		t.Fatalf("up skills: %d", n)
	}
}

func TestToggleSavesAndLinks(t *testing.T) {
	app, dir, cfg := setup(t)
	target := filepath.Join(dir, "target")
	// Unmanaged entries must survive.
	writeFile(t, filepath.Join(target, "own-file", "x"), "")
	if err := os.Symlink("/elsewhere", filepath.Join(target, "foreign")); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Link(); err != nil {
		t.Fatal(err)
	}
	got := links(t, target)
	if len(got) != 5 || got["gamma"] != filepath.Join(dir, "up", "group", "gamma") {
		t.Fatalf("initial links: %v", got)
	}

	if on, _, err := app.Toggle("own", "beta"); err != nil || on {
		t.Fatalf("toggle beta: on=%v err=%v", on, err)
	}
	if _, ok := links(t, target)["beta"]; ok {
		t.Fatal("beta still linked")
	}
	data, _ := os.ReadFile(cfg)
	for _, want := range []string{"# top comment", "# relative", "# mine", "# keep me", "disabled:\n      - beta"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("saved config lost %q:\n%s", want, data)
		}
	}

	// Enabling up/delta would clash with own/alpha: refused and not saved.
	if _, _, err := app.Toggle("up", "delta"); err == nil || !strings.Contains(err.Error(), "enabled twice") {
		t.Fatalf("expected conflict, got %v", err)
	}
	reloaded, _ := LoadConfig(cfg)
	if reloaded.Source("up").IsEnabled("delta") {
		t.Fatal("conflicting selection was saved")
	}

	if on, _, err := app.Toggle("own", "beta"); err != nil || !on {
		t.Fatalf("re-enable beta: on=%v err=%v", on, err)
	}
	if data, _ := os.ReadFile(cfg); strings.Contains(string(data), "disabled") {
		t.Fatalf("empty disabled list kept:\n%s", data)
	}
}

// A relative -config must still produce absolute link targets.
func TestRelativeConfigLinksAbsolute(t *testing.T) {
	_, dir, _ := setup(t)
	t.Chdir(dir)
	app := NewApp(filepath.Join("cfg", "skills.yaml"), nil)
	if err := app.Load(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Link(); err != nil {
		t.Fatal(err)
	}
	if got := links(t, filepath.Join(dir, "target"))["alpha"]; got != filepath.Join(dir, "own", "alpha") {
		t.Fatalf("alpha links to %q", got)
	}
}

func TestRemoveSource(t *testing.T) {
	app, dir, cfg := setup(t)
	target := filepath.Join(dir, "target")
	if _, err := app.Link(); err != nil {
		t.Fatal(err)
	}
	next, _, err := app.RemoveSource("up")
	if err != nil {
		t.Fatal(err)
	}
	if next.Find("up") != nil {
		t.Fatal("source still loaded")
	}
	if _, ok := links(t, target)["gamma"]; ok {
		t.Fatal("link of removed source kept")
	}
	if data, _ := os.ReadFile(cfg); strings.Contains(string(data), "name: up") || !strings.Contains(string(data), "# mine") {
		t.Fatalf("config after removal:\n%s", data)
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "skills.yaml")
	writeFile(t, cfg, "targets: []\nsources:\n  - name: a\n    path: x\n    git: y\n  - name: a\n    path: x\n    enable: all\n    enabled: [z]\n")
	_, err := LoadConfig(cfg)
	for _, want := range []string{"exactly one of path or git", "duplicate name", "uses disabled"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
}

func TestSaveKeepsBlankLines(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "skills.yaml")
	in := `# header

targets:
  - a

sources:
  - name: one
    path: x
    enable: all

  # second
  - name: two
    path: y
`
	writeFile(t, cfg, in)
	c, err := LoadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.Source("one").SetEnabled("s", false)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cfg)
	want := strings.Replace(in, "    enable: all\n", "    enable: all\n    disabled:\n      - s\n", 1)
	if string(data) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", data, want)
	}
}
