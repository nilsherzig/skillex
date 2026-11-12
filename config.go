package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnableMode decides how a source's skill lists are interpreted.
type EnableMode string

const (
	// EnableSelected links only the skills listed in Enabled (default).
	EnableSelected EnableMode = "selected"
	// EnableAll links every skill except those listed in Disabled.
	EnableAll EnableMode = "all"
)

// Source is one place skills come from: a local directory or a git repository.
type Source struct {
	Name     string     `yaml:"name"`
	Path     string     `yaml:"path,omitempty"`
	Git      string     `yaml:"git,omitempty"`
	Ref      string     `yaml:"ref,omitempty"`
	Optional bool       `yaml:"optional,omitempty"`
	Enable   EnableMode `yaml:"enable,omitempty"`
	Enabled  []string   `yaml:"enabled,omitempty"`
	Disabled []string   `yaml:"disabled,omitempty"`
}

// Config is the user-edited skills.yaml.
type Config struct {
	Targets []string `yaml:"targets"`
	Sources []Source `yaml:"sources"`

	path string    // real path of the config file
	doc  yaml.Node // original document, kept to preserve comments on save
}

func (s *Source) IsGit() bool { return s.Git != "" }

func (s *Source) mode() EnableMode {
	if s.Enable == "" {
		return EnableSelected
	}
	return s.Enable
}

// IsEnabled reports whether the skill with the given ID should be linked.
func (s *Source) IsEnabled(id string) bool {
	if s.mode() == EnableAll {
		return !slices.Contains(s.Disabled, id)
	}
	return slices.Contains(s.Enabled, id)
}

// SetEnabled records the wanted state in the list that matches the source's mode.
func (s *Source) SetEnabled(id string, on bool) {
	list := &s.Enabled
	include := on
	if s.mode() == EnableAll {
		list = &s.Disabled
		include = !on
	}
	*list = slices.DeleteFunc(*list, func(v string) bool { return v == id })
	if include {
		*list = append(*list, id)
		slices.Sort(*list)
	}
}

func defaultConfigPath() (string, error) {
	if p := os.Getenv("SKILLMAN_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "skillman", "skills.yaml"), nil
}

func LoadConfig(path string) (*Config, error) {
	real, err := filepath.EvalSymlinks(expandHome(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no config at %s: link configs/skillman from the dotfiles to ~/.config/skillman (configs/link.sh does this), or pass -config / set SKILLMAN_CONFIG", path)
	}
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return nil, err
	}
	cfg := &Config{path: real}
	if err := yaml.Unmarshal(data, &cfg.doc); err != nil {
		return nil, fmt.Errorf("%s: %w", real, err)
	}
	if err := cfg.doc.Decode(cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", real, err)
	}
	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	var errs []error
	seen := map[string]bool{}
	for i, s := range c.Sources {
		where := fmt.Sprintf("sources[%d] (%s)", i, s.Name)
		if s.Name == "" || strings.ContainsAny(s.Name, `/\`) || strings.HasPrefix(s.Name, ".") {
			errs = append(errs, fmt.Errorf("%s: name must be a plain non-empty word", where))
		}
		if seen[s.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name", where))
		}
		seen[s.Name] = true
		if (s.Path == "") == (s.Git == "") {
			errs = append(errs, fmt.Errorf("%s: set exactly one of path or git", where))
		}
		if s.Ref != "" && s.Git == "" {
			errs = append(errs, fmt.Errorf("%s: ref needs git", where))
		}
		switch s.mode() {
		case EnableAll:
			if len(s.Enabled) > 0 {
				errs = append(errs, fmt.Errorf("%s: enable: all uses disabled, not enabled", where))
			}
		case EnableSelected:
			if len(s.Disabled) > 0 {
				errs = append(errs, fmt.Errorf("%s: enable: selected uses enabled, not disabled", where))
			}
		default:
			errs = append(errs, fmt.Errorf("%s: enable must be all or selected", where))
		}
	}
	return errors.Join(errs...)
}

// Resolve turns a configured path into an absolute one. Relative paths are
// relative to the directory that holds the real config file.
func (c *Config) Resolve(p string) string {
	p = expandHome(os.ExpandEnv(p))
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(c.path), p)
	}
	return filepath.Clean(p)
}

func (c *Config) LockPath() string {
	return filepath.Join(filepath.Dir(c.path), "skills.lock")
}

func (c *Config) Source(name string) *Source {
	for i := range c.Sources {
		if c.Sources[i].Name == name {
			return &c.Sources[i]
		}
	}
	return nil
}

func (c *Config) sourcesNode() (*yaml.Node, error) {
	root := &c.doc
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	sources := mapValue(root, "sources")
	if sources == nil || sources.Kind != yaml.SequenceNode {
		return nil, errors.New("config: sources is not a list")
	}
	return sources, nil
}

// RemoveSource drops a source from the config; call Save to persist it.
func (c *Config) RemoveSource(name string) error {
	sources, err := c.sourcesNode()
	if err != nil {
		return err
	}
	sources.Content = slices.DeleteFunc(sources.Content, func(item *yaml.Node) bool {
		n := mapValue(item, "name")
		return n != nil && n.Value == name
	})
	c.Sources = slices.DeleteFunc(c.Sources, func(s Source) bool { return s.Name == name })
	return nil
}

// Save writes the enabled/disabled lists back into the original YAML document,
// leaving every other key and comment untouched.
func (c *Config) Save() error {
	sources, err := c.sourcesNode()
	if err != nil {
		return err
	}
	for _, item := range sources.Content {
		nameNode := mapValue(item, "name")
		if nameNode == nil {
			continue
		}
		s := c.Source(nameNode.Value)
		if s == nil {
			continue
		}
		setStringList(item, "enabled", s.Enabled)
		setStringList(item, "disabled", s.Disabled)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&c.doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return writeFileAtomic(c.path, []byte(spaceOut(buf.String())))
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func setStringList(m *yaml.Node, key string, values []string) {
	idx := -1
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			idx = i
			break
		}
	}
	if len(values) == 0 {
		if idx >= 0 {
			m.Content = slices.Delete(m.Content, idx, idx+2)
		}
		return
	}
	// Reuse existing item nodes so their comments survive.
	existing := map[string]*yaml.Node{}
	if idx >= 0 {
		for _, item := range m.Content[idx+1].Content {
			existing[item.Value] = item
		}
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, v := range values {
		item := existing[v]
		if item == nil {
			item = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
		}
		seq.Content = append(seq.Content, item)
	}
	if idx >= 0 {
		old := m.Content[idx+1]
		seq.HeadComment, seq.LineComment, seq.FootComment = old.HeadComment, old.LineComment, old.FootComment
		m.Content[idx+1] = seq
		return
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, seq)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if fi, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmp.Name(), fi.Mode().Perm())
	} else {
		_ = os.Chmod(tmp.Name(), 0o644)
	}
	return os.Rename(tmp.Name(), path)
}

// spaceOut restores the blank lines yaml.v3 drops: one before every top-level
// key and before every source after the first, placed above its comment block.
func spaceOut(text string) string {
	indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	isComment := func(s string) bool { return strings.HasPrefix(strings.TrimLeft(s, " "), "#") }
	var out []string
	for _, line := range strings.Split(text, "\n") {
		topKey := line != "" && indent(line) == 0 && !isComment(line)
		if topKey || strings.HasPrefix(line, "  - name:") {
			j := len(out)
			for j > 0 && isComment(out[j-1]) && indent(out[j-1]) == indent(line) {
				j--
			}
			if j > 0 && out[j-1] != "" && !strings.HasSuffix(out[j-1], ":") {
				out = slices.Insert(out, j, "")
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
