package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	styleHeader   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	styleMissing  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleOn       = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	styleCursor   = lipgloss.NewStyle().Reverse(true)
	styleError    = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	styleStatus   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleConfirm  = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	styleTitle    = lipgloss.NewStyle().Bold(true)
	styleFilterOn = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
)

// row is one line of the list: a source header or a skill below it.
type row struct {
	source string
	skill  *Skill // nil for a source header
}

func (r row) key() string {
	if r.skill == nil {
		return r.source
	}
	return r.source + "\x00" + r.skill.ID
}

type model struct {
	app *App

	rows   []row // rows matching the filter
	cursor int
	offset int

	filter    string
	filtering bool

	confirm string // source awaiting removal confirmation
	busy    string // running background action
	status  string
	isError bool

	width, height int
}

type actionDoneMsg struct {
	app     *App
	summary string
	err     error
}

func runTUI(app *App) error {
	// Anything written to the terminal behind bubbletea's back scrolls the
	// alternate screen and breaks the frame; the busy line shows progress.
	app.Log = func(string, ...any) {}
	m := &model{app: app, width: 80, height: 24}
	m.rebuild("")
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m *model) Init() tea.Cmd { return nil }

// rebuild recomputes the visible rows and keeps the cursor on keep if possible.
func (m *model) rebuild(keep string) {
	q := strings.ToLower(m.filter)
	m.rows = m.rows[:0]
	for i := range m.app.Sources {
		rs := &m.app.Sources[i]
		var skills []row
		for j := range rs.Skills {
			sk := &rs.Skills[j]
			hay := strings.ToLower(rs.Name + " " + sk.Name + " " + sk.ID + " " + sk.Description)
			if q == "" || strings.Contains(hay, q) {
				skills = append(skills, row{source: rs.Name, skill: sk})
			}
		}
		if q != "" && len(skills) == 0 && !strings.Contains(strings.ToLower(rs.Name), q) {
			continue
		}
		m.rows = append(m.rows, row{source: rs.Name})
		m.rows = append(m.rows, skills...)
	}
	m.cursor = min(m.cursor, max(len(m.rows)-1, 0))
	for i, r := range m.rows {
		if r.key() == keep {
			m.cursor = i
			break
		}
	}
}

func (m *model) current() (row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return row{}, false
	}
	return m.rows[m.cursor], true
}

func (m *model) setStatus(msg string, err error) {
	if err != nil {
		m.status, m.isError = err.Error(), true
		return
	}
	m.status, m.isError = msg, false
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case actionDoneMsg:
		m.busy = ""
		if msg.err == nil {
			keep := ""
			if r, ok := m.current(); ok {
				keep = r.key()
			}
			m.app = msg.app
			m.rebuild(keep)
		}
		m.setStatus(msg.summary, msg.err)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.confirm != "" {
		name := m.confirm
		m.confirm = ""
		if k.String() == "y" || k.String() == "Y" {
			m.busy = "removing " + name
			app := m.app
			return m, func() tea.Msg {
				next, summary, err := app.RemoveSource(name)
				return actionDoneMsg{next, summary, err}
			}
		}
		m.setStatus("kept "+name, nil)
		return m, nil
	}
	if m.filtering {
		keep := ""
		if r, ok := m.current(); ok {
			keep = r.key()
		}
		switch k.Type {
		case tea.KeyEnter:
			m.filtering = false
		case tea.KeyEsc:
			m.filtering, m.filter = false, ""
		case tea.KeyBackspace:
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
			keep = ""
		case tea.KeyRunes, tea.KeySpace:
			m.filter += string(k.Runes)
			keep = ""
		case tea.KeyUp, tea.KeyDown:
			m.move(map[tea.KeyType]int{tea.KeyUp: -1, tea.KeyDown: 1}[k.Type])
			return m, nil
		}
		m.rebuild(keep)
		if keep == "" {
			m.firstSkill()
		}
		return m, nil
	}

	switch k.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		if m.filter != "" {
			m.filter = ""
			r, _ := m.current()
			m.rebuild(r.key())
			return m, nil
		}
		return m, tea.Quit
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup", "ctrl+u":
		m.move(-m.listHeight() / 2)
	case "pgdown", "ctrl+d":
		m.move(m.listHeight() / 2)
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = max(len(m.rows)-1, 0)
	case "tab":
		m.jumpSource(1)
	case "shift+tab":
		m.jumpSource(-1)
	case "/":
		m.filtering = true
	case " ", "x":
		if m.busy != "" {
			break
		}
		r, ok := m.current()
		if !ok || r.skill == nil {
			break
		}
		on, rep, err := m.app.Toggle(r.source, r.skill.ID)
		state := "disabled"
		if on {
			state = "enabled"
		}
		m.setStatus(fmt.Sprintf("%s %s (links +%d -%d)%s", state, r.skill.Name, rep.Added, rep.Removed, warnSuffix(rep)), err)
	case "u":
		if m.busy != "" {
			break
		}
		r, ok := m.current()
		if !ok {
			break
		}
		name, app := r.source, m.app
		m.busy = "updating " + name
		return m, func() tea.Msg {
			next, summary, err := app.UpdateSource(name)
			return actionDoneMsg{next, summary, err}
		}
	case "d":
		if m.busy != "" {
			break
		}
		if r, ok := m.current(); ok && r.skill == nil {
			m.confirm = r.source
		} else {
			m.setStatus("select a source header to remove it", nil)
		}
	}
	return m, nil
}

func warnSuffix(rep LinkReport) string {
	if len(rep.Warnings) == 0 {
		return ""
	}
	return " — " + strings.Join(rep.Warnings, "; ")
}

func (m *model) move(delta int) {
	m.cursor = min(max(m.cursor+delta, 0), max(len(m.rows)-1, 0))
}

func (m *model) jumpSource(dir int) {
	for i := m.cursor + dir; i >= 0 && i < len(m.rows); i += dir {
		if m.rows[i].skill == nil {
			m.cursor = i
			return
		}
	}
}

// listHeight is the number of rows available for the list.
func (m *model) listHeight() int {
	return max(m.height-4, 1) // title, blank, status, help
}

func (m *model) View() string {
	var b strings.Builder
	title := styleTitle.Render("skillman") + styleDim.Render("  "+m.app.Cfg.path)
	if m.filtering || m.filter != "" {
		cursor := ""
		if m.filtering {
			cursor = "▏"
		}
		title += "  " + styleFilterOn.Render("/"+m.filter+cursor)
	}
	b.WriteString(ansi.Truncate(title, m.width, "…") + "\n\n")

	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = min(m.offset, max(len(m.rows)-h, 0))

	lines := 0
	for i := m.offset; i < len(m.rows) && lines < h; i++ {
		b.WriteString(m.renderRow(m.rows[i], i == m.cursor) + "\n")
		lines++
	}
	if len(m.rows) == 0 {
		b.WriteString(styleDim.Render("  no matches") + "\n")
		lines++
	}
	for ; lines < h; lines++ {
		b.WriteString("\n")
	}

	switch {
	case m.confirm != "":
		rs := m.app.Find(m.confirm)
		what := "its entry in skills.yaml and its links"
		if rs != nil && rs.IsGit() {
			what = "its entry in skills.yaml, its lock entry, its links and the clone"
		}
		b.WriteString(styleConfirm.Render(fmt.Sprintf("Remove source %s? This deletes %s. [y/N]", m.confirm, what)))
	case m.busy != "":
		b.WriteString(styleStatus.Render(m.busy + " …"))
	case m.isError:
		b.WriteString(styleError.Render(ansi.Truncate(m.status, m.width, "…")))
	default:
		b.WriteString(styleStatus.Render(ansi.Truncate(m.status, m.width, "…")))
	}
	b.WriteString("\n")
	help := "space toggle · u update source · d remove source · / filter · tab next source · q quit"
	b.WriteString(styleDim.Render(ansi.Truncate(help, m.width, "…")))
	return b.String()
}

func (m *model) renderRow(r row, selected bool) string {
	rs := m.app.Find(r.source)
	var line string
	if r.skill == nil {
		head := styleHeader.Render("▌ " + r.source)
		info := sourceSummary(rs)
		info = strings.TrimPrefix(info, rs.Name)
		if rs.Missing {
			line = head + styleMissing.Render(info)
		} else {
			line = head + styleDim.Render(info)
		}
	} else {
		box := "[ ]"
		if rs.IsEnabled(r.skill.ID) {
			box = styleOn.Render("[x]")
		}
		name := r.skill.Name
		if r.skill.ID != name && r.skill.ID != "." {
			name += styleDim.Render(" (" + r.skill.ID + ")")
		}
		line = "  " + box + " " + name
		if r.skill.Description != "" {
			line += styleDim.Render("  " + r.skill.Description)
		}
	}
	line = ansi.Truncate(line, m.width, "…")
	if selected {
		pad := max(m.width-ansi.StringWidth(line), 0)
		line = styleCursor.Render(ansi.Strip(line) + strings.Repeat(" ", pad))
	}
	return line
}

// firstSkill puts the cursor on the first skill row, e.g. after the filter changed.
func (m *model) firstSkill() {
	for i, r := range m.rows {
		if r.skill != nil {
			m.cursor = i
			return
		}
	}
	m.cursor = 0
}
