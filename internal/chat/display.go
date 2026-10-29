package chat

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"golang.org/x/term"

	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

var (
	bold    = lipgloss.NewStyle().Bold(true)
	dim     = lipgloss.NewStyle().Faint(true)
	italic  = lipgloss.NewStyle().Italic(true)
	cyan    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	magenta = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	yellow  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	green   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	red     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	blue    = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
)

func termWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 20 {
		return w
	}
	return 80
}

// panel draws content in a rounded box with title set into the top border.
func panel(title, content string, border lipgloss.TerminalColor) string {
	width := termWidth()
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1).
		Width(width - 2).
		Render(content)

	if title == "" {
		return box
	}
	lines := strings.SplitN(box, "\n", 2)
	edge := lipgloss.NewStyle().Foreground(border)
	fill := max(width-lipgloss.Width(title)-6, 0)
	lines[0] = edge.Render("╭─ ") + title + edge.Render(" "+strings.Repeat("─", fill)+"╮")
	return strings.Join(lines, "\n")
}

// rule draws a horizontal line with title centered in it.
func rule(title string) string {
	width := termWidth()
	side := max((width-lipgloss.Width(title)-2)/2, 0)
	line := blue.Render(strings.Repeat("─", side))
	return line + " " + title + " " + line
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func orDash(items []string) string {
	if len(items) == 0 {
		return "—"
	}
	return strings.Join(items, ", ")
}

func workersTable(workers []schemas.WorkerInfo) string {
	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(blue).
		BorderRow(true).
		Headers("Worker ID", "Model", "Load", "Tools", "Tags", "Status", "URL").
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().Padding(0, 1)
			if row == table.HeaderRow {
				return s.Bold(true)
			}
			switch col {
			case 0:
				return s.Inherit(cyan)
			case 1:
				return s.Inherit(magenta)
			case 2, 5:
				return s.Align(lipgloss.Center)
			}
			return s.Inherit(dim)
		})

	for _, w := range workers {
		status := bold.Inherit(red).Render("✗ gone")
		if w.Alive {
			status = bold.Inherit(green).Render("✓ alive")
		}
		load := fmt.Sprintf("%d/%d", w.InFlight, w.Slots)
		t.Row(w.WorkerID, w.ModelID, load, orDash(w.Tools), orDash(w.Tags), status, w.URL)
	}
	return t.Render()
}

func jobsTable(jobs []schemas.Job) string {
	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(blue).
		Headers("When", "Status", "Subtasks", "Prompt", "Job ID").
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().Padding(0, 1)
			if row == table.HeaderRow {
				return s.Bold(true)
			}
			if col == 4 {
				return s.Inherit(dim)
			}
			return s
		})
	for _, j := range jobs {
		t.Row(j.CreatedAt.Local().Format("15:04:05"), string(j.Status), fmt.Sprint(len(j.Subtasks)),
			truncate(strings.ReplaceAll(j.Prompt, "\n", " "), 50), j.ID)
	}
	return t.Render()
}

// subtaskLine describes one planned subtask.
func subtaskLine(st schemas.SubTask) string {
	line := fmt.Sprintf("  %s  %s", cyan.Render(st.ID), st.Title)
	if len(st.DependsOn) > 0 {
		line += dim.Render("  after " + strings.Join(st.DependsOn, ", "))
	}
	if len(st.Requires) > 0 {
		line += dim.Render("  needs " + strings.Join(st.Requires, ", "))
	}
	return line
}

// resultPanel shows a finished subtask's result.
func resultPanel(r *schemas.AgentResult) string {
	who := r.SubtaskID
	if r.WorkerID != "" {
		who += " on " + r.WorkerID
	}
	if !r.Success {
		header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("52")).
			Render(" " + who + " — ERROR ")
		errMsg := ""
		if r.Error != nil {
			errMsg = *r.Error
		}
		return panel(header, errMsg, lipgloss.Color("1"))
	}
	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("22")).
		Render(" " + who + " — result ")
	out := panel(header, truncate(strings.TrimSpace(r.Content), 400), lipgloss.Color("2"))
	if line := metricsLine(r.Metrics); line != "" {
		out += "\n  " + line
	}
	return out
}

// toolCallLine describes a tool call, with its arguments shortened.
func toolCallLine(e schemas.Event) string {
	keys := make([]string, 0, len(e.Args))
	for k := range e.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := make([]string, 0, len(keys))
	for _, k := range keys {
		v := strings.ReplaceAll(fmt.Sprint(e.Args[k]), "\n", "⏎")
		args = append(args, k+"="+truncate(v, 40))
	}
	return dim.Render(fmt.Sprintf("    ↳ %s %s: ", e.SubtaskID, e.WorkerID)) +
		yellow.Render(e.Tool) + dim.Render("("+strings.Join(args, ", ")+")")
}

var metricOrder = []string{"duration", "output_tokens", "tool_calls", "steps"}

// metricsLine shows the most useful metrics.
func metricsLine(metrics map[string]any) string {
	var items []string
	for _, k := range metricOrder {
		v, ok := metrics[k]
		if !ok {
			continue
		}
		if k == "tool_calls" || k == "steps" {
			if f, _ := v.(float64); f == 0 || (k == "steps" && f == 1) {
				continue
			}
		}
		items = append(items, dim.Render(k+":")+" "+formatMetric(k, v))
	}
	return strings.Join(items, "  ·  ")
}

func formatMetric(key string, v any) string {
	f, ok := v.(float64)
	if !ok {
		return fmt.Sprint(v)
	}
	if key == "duration" {
		return (time.Duration(f * float64(time.Second))).Round(100 * time.Millisecond).String()
	}
	if f == float64(int64(f)) {
		return fmt.Sprint(int64(f))
	}
	return fmt.Sprintf("%.2f", f)
}

// markdownStyle is a glamour style name. It is not auto-detected because detection
// queries the terminal, which can swallow typed input on terminals that don't answer.
var markdownStyle = "dark"

func initMarkdownStyle() {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		markdownStyle = "notty"
	} else if s := os.Getenv("GLAMOUR_STYLE"); s != "" {
		markdownStyle = s
	}
}

func renderMarkdown(md string) string {
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(markdownStyle), glamour.WithWordWrap(termWidth()-4))
	if err != nil {
		return md + "\n"
	}
	out, err := r.Render(md)
	if err != nil {
		return md + "\n"
	}
	return out
}

func printHelp() {
	commands := [][2]string{
		{"/workers", "Show registered workers and their load"},
		{"/jobs", "Show recent jobs in this conversation"},
		{"/clear", "Start a new conversation"},
		{"/help", "Show this help message"},
		{"/quit", "Exit the chat"},
	}
	var b strings.Builder
	for i, c := range commands {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(bold.Inherit(cyan).Width(10).Render(c[0]) + "  " + dim.Render(c[1]))
	}
	b.WriteString("\n\n" + dim.Render("Press Ctrl-C while a job runs to cancel it."))
	fmt.Println(panel(bold.Render("Slash Commands"), b.String(), lipgloss.Color("4")))
}
