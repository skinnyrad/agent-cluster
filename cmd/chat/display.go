package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

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

func printWorkersTable(baseURL string) {
	fmt.Println()
	status := startStatus("Fetching worker status...")
	data, err := fetchWorkers(baseURL)
	status.Stop()
	if err != nil {
		fmt.Println(bold.Inherit(red).Render("Error fetching workers:"), err)
		return
	}
	if len(data.Workers) == 0 {
		fmt.Println(yellow.Render("No workers registered."))
		return
	}

	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(blue).
		BorderRow(true).
		Headers("Worker ID", "URL", "Model", "Tags", "Alive", "Enabled").
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().Padding(0, 1)
			if row == table.HeaderRow {
				return s.Bold(true)
			}
			switch col {
			case 0:
				return s.Inherit(cyan)
			case 1, 3:
				return s.Inherit(dim)
			case 2:
				return s.Inherit(magenta)
			}
			return s.Align(lipgloss.Center)
		})

	for _, w := range data.Workers {
		alive := bold.Inherit(red).Render("✗ dead")
		if w.Alive {
			alive = bold.Inherit(green).Render("✓ alive")
		}
		enabled := red.Render("no")
		if w.Enabled {
			enabled = green.Render("yes")
		}
		tags := strings.Join(w.Tags, ", ")
		if tags == "" {
			tags = "—"
		}
		model := w.ModelID
		if model == "" {
			model = "?"
		}
		t.Row(w.WorkerID, w.URL, model, tags, alive, enabled)
	}

	fmt.Println(bold.Render("Worker Fleet Status"))
	fmt.Println(t.Render())
	fmt.Println()
}

// printDispatchResult prints the subtask plan, each worker's result, then the synthesis.
func printDispatchResult(data *schemas.DispatchResponse) {
	// Subtask plan
	fmt.Printf("\n%s  %s\n", bold.Inherit(green).Render("✓ Done!"), dim.Render("request_id: "+data.RequestID))
	fmt.Println(bold.Render("Created ") + bold.Inherit(cyan).Render(fmt.Sprint(len(data.Subtasks))) + bold.Render(" subtask(s):"))
	for _, st := range data.Subtasks {
		fmt.Printf("  %s  role=%s\n", cyan.Render(st.WorkerID), dim.Render(truncate(st.SystemPrompt, 60)))
		fmt.Printf("        task=%s\n", italic.Render(truncate(st.TaskPrompt, 80)))
	}

	// Per-worker results
	fmt.Println()
	for _, r := range data.Results {
		if r.Success {
			header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("22")).
				Render(fmt.Sprintf(" Worker %s — result ", r.WorkerID))
			fmt.Println(panel(header, truncate(r.Content, 400), lipgloss.Color("2")))
			if line := metricsLine(r.Metrics); line != "" {
				fmt.Println("  " + line)
			}
		} else {
			header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("52")).
				Render(fmt.Sprintf(" Worker %s — ERROR ", r.WorkerID))
			errMsg := ""
			if r.Error != nil {
				errMsg = *r.Error
			}
			fmt.Println(panel(header, errMsg, lipgloss.Color("1")))
		}
	}

	// Synthesis
	fmt.Println()
	fmt.Println(rule(bold.Inherit(blue).Render("Fleet Synthesis")))
	if data.Synthesis == "" {
		fmt.Println(yellow.Render("(no synthesis returned)"))
	} else {
		fmt.Print(renderMarkdown(data.Synthesis))
	}
	fmt.Println()
}

// metricsLine shows up to four metrics, in a stable order.
func metricsLine(metrics map[string]any) string {
	keys := make([]string, 0, len(metrics))
	for k := range metrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 4 {
		keys = keys[:4]
	}
	items := make([]string, 0, len(keys))
	for _, k := range keys {
		items = append(items, dim.Render(k+":")+" "+formatMetric(metrics[k]))
	}
	return strings.Join(items, "  ·  ")
}

func formatMetric(v any) string {
	if f, ok := v.(float64); ok {
		if f == float64(int64(f)) {
			return fmt.Sprint(int64(f))
		}
		return fmt.Sprintf("%.2f", f)
	}
	return fmt.Sprint(v)
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
		{"/workers", "Show live worker status table"},
		{"/clear", "Clear conversation history"},
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
	fmt.Println(panel(bold.Render("Slash Commands"), b.String(), lipgloss.Color("4")))
}
