// Command skillex links agent skills from local directories and pinned git
// repositories into agent homes, driven by skills.yaml.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

const usage = `Usage: skillex [-config FILE] [command]

Commands:
  (none), tui        interactive selection (space toggles, u updates, d removes a source)
  sync               check out locked versions and relink all targets
  update [SOURCE..]  move git sources (default: all) to their latest ref, then relink
  list               print all skills and whether they are enabled

The config defaults to $SKILLEX_CONFIG or ~/.config/skillex/skills.yaml.
The lock file skills.lock lives next to the real config file.
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "skillex:", err)
		os.Exit(1)
	}
}

func run() error {
	defPath, err := defaultConfigPath()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("skillex", flag.ContinueOnError)
	cfgPath := fs.String("config", defPath, "path to skills.yaml")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	args := fs.Args()
	cmd := "tui"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}

	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
	app := NewApp(*cfgPath, logf)

	switch cmd {
	case "tui":
		if err := app.Load(nil); err != nil {
			return err
		}
		return runTUI(app)
	case "sync":
		if err := app.Load(nil); err != nil {
			return err
		}
		return printLink(app.Link())
	case "update":
		if err := app.Load(nil); err != nil {
			return err
		}
		names := args
		if len(names) == 0 {
			for _, rs := range app.Sources {
				if rs.IsGit() {
					names = append(names, rs.Name)
				}
			}
		}
		for _, name := range names {
			if app.Find(name) == nil {
				return fmt.Errorf("unknown source %s", name)
			}
			next, summary, err := app.UpdateSource(name)
			if err != nil {
				return err
			}
			fmt.Println(summary)
			app = next
		}
		return nil
	case "list":
		if err := app.Load(nil); err != nil {
			return err
		}
		for _, rs := range app.Sources {
			fmt.Println(sourceSummary(&rs))
			for _, sk := range rs.Skills {
				mark := " "
				if rs.IsEnabled(sk.ID) {
					mark = "x"
				}
				fmt.Printf("  [%s] %-28s %s\n", mark, sk.Name, sk.ID)
			}
		}
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fs.Usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func printLink(rep LinkReport, err error) error {
	for _, w := range rep.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	if err != nil {
		return err
	}
	fmt.Printf("links +%d -%d\n", rep.Added, rep.Removed)
	if len(rep.Warnings) > 0 {
		return fmt.Errorf("%d link conflict(s) left unresolved", len(rep.Warnings))
	}
	return nil
}

func sourceSummary(rs *ResolvedSource) string {
	enabled := 0
	for _, sk := range rs.Skills {
		if rs.IsEnabled(sk.ID) {
			enabled++
		}
	}
	var where string
	switch {
	case rs.Missing:
		where = "missing: " + rs.Root
	case rs.IsGit():
		where = strings.TrimSuffix(strings.TrimPrefix(rs.Git, "https://"), ".git")
		if rs.Ref != "" {
			where += " " + rs.Ref
		}
		where += " @ " + short(rs.Commit)
	default:
		where = rs.Root
	}
	return fmt.Sprintf("%s  %s  (%d/%d, %s)", rs.Name, where, enabled, len(rs.Skills), rs.mode())
}
