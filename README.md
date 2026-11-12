# skillex

Links agent skills (directories with a `SKILL.md`) from local directories and
git repositories into agent homes such as `~/.claude/skills`.

```sh
skillex                  # TUI: space toggles a skill, u updates and d removes a source
skillex sync             # check out locked versions and relink
skillex update [SOURCE]  # move git sources to their latest ref
skillex list
```

The config is `~/.config/skillex/skills.yaml` (or `-config` / `$SKILLEX_CONFIG`);
git sources are pinned in `skills.lock` next to it and cloned to
`~/.local/share/skillex/repos`. Only symlinks that point into a configured
source are changed.

```yaml
targets:
  - ~/.claude/skills
sources:
  - name: own
    path: ~/skills          # relative paths resolve against the config file
    enable: all             # link everything except `disabled`
    disabled: [draft-skill]
  - name: mattpocock
    git: https://github.com/mattpocock/skills
    ref: main
    enabled:                # default mode: link only these
      - skills/productivity/grill-me
```
