package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LinkReport summarises what Apply changed.
type LinkReport struct {
	Added, Removed int
	Warnings       []string
}

// DesiredLinks maps link names to skill directories for all enabled skills.
func DesiredLinks(sources []ResolvedSource) (map[string]Skill, error) {
	want := map[string]Skill{}
	var errs []error
	for _, rs := range sources {
		for _, sk := range rs.Skills {
			if !rs.IsEnabled(sk.ID) {
				continue
			}
			if prev, ok := want[sk.Name]; ok {
				errs = append(errs, fmt.Errorf("skill name %q is enabled twice: %s:%s and %s:%s",
					sk.Name, prev.Source, prev.ID, sk.Source, sk.ID))
				continue
			}
			want[sk.Name] = sk
		}
	}
	return want, errors.Join(errs...)
}

// ManagedRoots are the directories whose links skillman owns. Links into them
// that are not wanted are removed; everything else in a target is left alone.
func ManagedRoots(cfg *Config, sources []ResolvedSource, extra ...string) []string {
	roots := append([]string{}, extra...)
	if cache, err := cacheDir(); err == nil {
		roots = append(roots, cache)
	}
	for _, rs := range sources {
		if !rs.IsGit() {
			roots = append(roots, rs.Root)
		}
	}
	return roots
}

func within(path string, roots []string) bool {
	for _, r := range roots {
		if path == r || strings.HasPrefix(path, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Apply makes every target contain exactly the wanted skill links, touching only
// symlinks that point into a managed root.
func Apply(cfg *Config, sources []ResolvedSource, extraRoots ...string) (LinkReport, error) {
	var rep LinkReport
	want, err := DesiredLinks(sources)
	if err != nil {
		return rep, err // never half-apply a conflicting selection
	}
	roots := ManagedRoots(cfg, sources, extraRoots...)
	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, t := range cfg.Targets {
		target := cfg.Resolve(t)
		if fi, err := os.Lstat(target); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s is a symlink, skipped (replace it with a directory)", target))
			continue
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return rep, err
		}
		entries, err := os.ReadDir(target)
		if err != nil {
			return rep, err
		}
		for _, e := range entries {
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			link := filepath.Join(target, e.Name())
			dest, err := os.Readlink(link)
			if err != nil || !within(filepath.Clean(dest), roots) {
				continue
			}
			if sk, ok := want[e.Name()]; ok && filepath.Clean(dest) == sk.Dir {
				continue
			}
			if err := os.Remove(link); err != nil {
				return rep, err
			}
			rep.Removed++
		}
		for _, name := range names {
			link := filepath.Join(target, name)
			dir := want[name].Dir
			if _, err := os.Lstat(link); err == nil {
				if dest, _ := os.Readlink(link); filepath.Clean(dest) != dir {
					rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s exists and is not managed, skipped", link))
				}
				continue
			}
			if err := os.Symlink(dir, link); err != nil {
				return rep, err
			}
			rep.Added++
		}
	}
	return rep, nil
}
