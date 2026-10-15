package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	barStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)

type fetchResult struct {
	md, title string
	cached    bool
	via       string
	err       error
}

type tuiModel struct {
	state    string // input | loading | view
	input    string
	url      string
	title    string
	lines    []string
	offset   int
	w, h     int
	budget   int
	opts     fetchOpts
	jsonMode bool
	cached   bool
	status   string
	search   string
	seeking  bool
	help     bool
}

func runTUI(initialURL string, o fetchOpts) error {
	m := tuiModel{state: "input", budget: o.budget, opts: o}
	if initialURL != "" {
		m.state = "loading"
		m.url = initialURL
		m.input = initialURL
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m tuiModel) Init() tea.Cmd {
	if m.state == "loading" {
		return fetchCmd(m.url, m.opts)
	}
	return nil
}

func fetchCmd(url string, o fetchOpts) tea.Cmd {
	return func() tea.Msg {
		md, title, cached, via, err := get(url, o)
		return fetchResult{md, title, cached, via, err}
	}
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case fetchResult:
		if msg.err != nil {
			m.state = "input"
			m.status = msg.err.Error()
			return m, nil
		}
		body := applyBudget(msg.md, m.budget)
		if m.jsonMode {
			body = "{\n  \"url\": \"" + m.url + "\",\n  \"title\": \"" + msg.title + "\"\n}\n" + body
		}
		m.title, m.lines, m.offset = msg.title, strings.Split(body, "\n"), 0
		m.cached, m.state, m.status = msg.cached, "view", ""
		return m, nil
	case tea.KeyMsg:
		k := msg.String()
		if k == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.state {
		case "input":
			switch k {
			case "enter":
				if strings.TrimSpace(m.input) == "" {
					return m, nil
				}
				m.url, m.state, m.status = normalizeURL(strings.TrimSpace(m.input)), "loading", ""
				return m, fetchCmd(m.url, m.opts)
			case "backspace":
				if len(m.input) > 0 {
					m.input = m.input[:len(m.input)-1]
				}
				return m, nil
			case "esc":
				return m, tea.Quit
			default:
				if len(k) == 1 {
					m.input += k
				}
				return m, nil
			}
		case "view":
			limit := max(0, len(m.lines)-(m.h-6))
			switch {
			case m.seeking:
				switch k {
				case "enter", "esc":
					m.seeking = false
					return m, nil
				case "backspace":
					if len(m.search) > 0 {
						m.search = m.search[:len(m.search)-1]
					}
					return m, nil
				default:
					if len(k) == 1 {
						m.search += k
						for i, l := range m.lines {
							if strings.Contains(strings.ToLower(l), strings.ToLower(m.search)) {
								m.offset = min(i, limit)
								break
							}
						}
					}
					return m, nil
				}
			}
			switch k {
			case "q", "esc":
				return m, tea.Quit
			case "j", "down":
				m.offset = min(m.offset+1, limit)
			case "k", "up":
				m.offset = max(m.offset-1, 0)
			case "g":
				m.offset = 0
			case "G":
				m.offset = limit
			case "pgdown", " ":
				m.offset = min(m.offset+(m.h-6), limit)
			case "pgup":
				m.offset = max(m.offset-(m.h-6), 0)
			case "/":
				m.seeking, m.search = true, ""
			case "J", "tab":
				m.jsonMode = !m.jsonMode
				m.state = "loading"
				return m, fetchCmd(m.url, m.opts)
			case "r":
				m.state = "loading"
				return m, fetchCmd(m.url, m.opts)
			case "b":
				m.input, m.state = m.url, "input"
			case "?":
				m.help = !m.help
			}
			return m, nil
		case "loading":
			if k == "esc" {
				m.state = "input"
			}
			return m, nil
		}
	}
	return m, nil
}

func (m tuiModel) View() string {
	switch m.state {
	case "input":
		s := titleStyle.Render("picu — curl for the agent era") + "\n\n"
		s += "URL: " + m.input + "█\n\n"
		s += statusStyle.Render("enter fetch · esc quit   e.g. https://example.com/article") + "\n"
		if m.status != "" {
			s += "\n" + errStyle.Render(m.status) + "\n"
		}
		return s
	case "loading":
		return titleStyle.Render("fetching "+m.url+" …") + "\n" + statusStyle.Render("esc cancel") + "\n"
	case "view":
		end := min(m.offset+(m.h-6), len(m.lines))
		body := strings.Join(m.lines[m.offset:end], "\n")
		badge := "fresh"
		if m.cached {
			badge = "cached"
		}
		head := titleStyle.Render(m.title) + "  " + statusStyle.Render(fmt.Sprintf("%s · %d/%d lines · budget %d", badge, end, len(m.lines), m.budget))
		bar := barStyle.Render("j/k scroll · / search · J json · r refetch · b url · ? help · q quit")
		if m.seeking {
			bar = "search: " + m.search + "█  (enter done)"
		}
		if m.help {
			bar += "\n" + statusStyle.Render("tui v0.1: static fetch, budget trim, disk cache. lightpanda sidecar + profiles land in v0.2")
		}
		return head + "\n\n" + body + "\n\n" + bar + "\n"
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
