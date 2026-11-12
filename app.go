package main

import (
	"fmt"
	"os"
	"strings"
)

// App bundles the loaded config, lock and resolved sources. Operations that
// change state write files and links immediately.
type App struct {
	cfgPath string
	Cfg     *Config
	Lock    *Lock
	Sources []ResolvedSource
	Log     func(string, ...any)
}

func NewApp(cfgPath string, log func(string, ...any)) *App {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &App{cfgPath: cfgPath, Log: log}
}

// Load reads config and lock from disk and resolves all sources. Sources named
// in update are fetched and moved forward; the lock is saved when it changed.
func (a *App) Load(update map[string]bool) error {
	cfg, err := LoadConfig(a.cfgPath)
	if err != nil {
		return err
	}
	lock, err := LoadLock(cfg.LockPath())
	if err != nil {
		return err
	}
	sources, changed, err := ResolveSources(cfg, lock, update, a.Log)
	if err != nil {
		return err
	}
	// Drop entries for sources that are no longer configured.
	for name := range lock.Sources {
		if s := cfg.Source(name); s == nil || !s.IsGit() {
			delete(lock.Sources, name)
			changed = true
		}
	}
	if changed {
		if err := lock.Save(cfg.LockPath()); err != nil {
			return err
		}
	}
	a.Cfg, a.Lock, a.Sources = cfg, lock, sources
	return nil
}

func (a *App) Find(source string) *ResolvedSource {
	for i := range a.Sources {
		if a.Sources[i].Name == source {
			return &a.Sources[i]
		}
	}
	return nil
}

func (a *App) Link(extraRoots ...string) (LinkReport, error) {
	return Apply(a.Cfg, a.Sources, extraRoots...)
}

// Toggle flips one skill, saves the config and relinks. On a link conflict
// the change is reverted so the config never holds an unapplicable selection.
func (a *App) Toggle(source, id string) (bool, LinkReport, error) {
	src := a.Cfg.Source(source)
	if src == nil {
		return false, LinkReport{}, fmt.Errorf("unknown source %s", source)
	}
	on := !src.IsEnabled(id)
	src.SetEnabled(id, on)
	if _, err := DesiredLinks(a.Sources); err != nil {
		src.SetEnabled(id, !on)
		return !on, LinkReport{}, err
	}
	if err := a.Cfg.Save(); err != nil {
		src.SetEnabled(id, !on)
		return !on, LinkReport{}, err
	}
	rep, err := a.Link()
	return on, rep, err
}

// UpdateSource loads a fresh App with the source moved to its latest ref and
// relinks. The receiver is only read, so it can keep serving a UI meanwhile.
func (a *App) UpdateSource(name string) (*App, string, error) {
	old := a.Find(name)
	next := NewApp(a.cfgPath, a.Log)
	if err := next.Load(map[string]bool{name: true}); err != nil {
		return nil, "", err
	}
	cur := next.Find(name)
	if cur == nil {
		return nil, "", fmt.Errorf("unknown source %s", name)
	}
	rep, err := next.Link()
	if err != nil {
		return nil, "", err
	}
	return next, describeUpdate(old, cur, rep), nil
}

func describeUpdate(old, cur *ResolvedSource, rep LinkReport) string {
	var parts []string
	if cur.IsGit() {
		if old != nil && old.Commit != "" && old.Commit != cur.Commit {
			parts = append(parts, fmt.Sprintf("%s → %s", short(old.Commit), short(cur.Commit)))
			if changed := changedSkills(cur, old.Commit); len(changed) > 0 {
				parts = append(parts, "changed: "+strings.Join(changed, ", "))
			}
		} else {
			parts = append(parts, "up to date at "+short(cur.Commit))
		}
	} else {
		parts = append(parts, "rescanned")
	}
	if old != nil {
		before := map[string]bool{}
		for _, sk := range old.Skills {
			before[sk.ID] = true
		}
		var added []string
		for _, sk := range cur.Skills {
			if !before[sk.ID] {
				added = append(added, sk.ID)
			}
			delete(before, sk.ID)
		}
		if len(added) > 0 {
			parts = append(parts, "new: "+strings.Join(added, ", "))
		}
		if len(before) > 0 {
			parts = append(parts, fmt.Sprintf("%d gone", len(before)))
		}
	}
	if rep.Added+rep.Removed > 0 {
		parts = append(parts, fmt.Sprintf("links +%d -%d", rep.Added, rep.Removed))
	}
	return cur.Name + ": " + strings.Join(parts, "; ")
}

// changedSkills lists the skills whose files differ between from and HEAD.
func changedSkills(rs *ResolvedSource, from string) []string {
	out, err := git(rs.Root, "diff", "--name-only", from, rs.Commit)
	if err != nil || out == "" {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, file := range strings.Split(out, "\n") {
		for _, sk := range rs.Skills {
			if (sk.ID == "." || strings.HasPrefix(file, sk.ID+"/")) && !seen[sk.ID] {
				seen[sk.ID] = true
				ids = append(ids, sk.ID)
			}
		}
	}
	return ids
}

// RemoveSource deletes a source from the config, removes its links and, for
// git sources, its clone. It returns a freshly loaded App.
func (a *App) RemoveSource(name string) (*App, string, error) {
	rs := a.Find(name)
	if rs == nil {
		return nil, "", fmt.Errorf("unknown source %s", name)
	}
	root, isGit := rs.Root, rs.IsGit()
	// Edit a fresh copy so the receiver stays valid for a UI that still renders it.
	cfg, err := LoadConfig(a.cfgPath)
	if err != nil {
		return nil, "", err
	}
	if err := cfg.RemoveSource(name); err != nil {
		return nil, "", err
	}
	if err := cfg.Save(); err != nil {
		return nil, "", err
	}
	next := NewApp(a.cfgPath, a.Log)
	if err := next.Load(nil); err != nil { // also drops the lock entry
		return nil, "", err
	}
	rep, err := next.Link(root)
	if err != nil {
		return nil, "", err
	}
	if isGit {
		if err := os.RemoveAll(root); err != nil {
			return nil, "", err
		}
	}
	return next, fmt.Sprintf("removed %s (links -%d)", name, rep.Removed), nil
}
