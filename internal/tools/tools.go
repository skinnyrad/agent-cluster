// Package tools provides the tools a worker's agent can call. File and command tools are
// confined to a workspace directory; anything outside it is refused.
//
// Tool sets:
//
//	files  list_files, read_file, write_file
//	shell  run_command (runs a shell command in the workspace; not a security sandbox)
//	web    fetch_url
package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/skinnyrad/agent-cluster/internal/agent"
)

// Sets lists the tool sets Build accepts.
var Sets = []string{"files", "shell", "web"}

// Config configures the tools Build creates.
type Config struct {
	Workspace      string        // directory the tools may read and write
	CommandTimeout time.Duration // limit for one run_command call
	HTTPClient     *http.Client  // used by fetch_url
}

// Build returns the tools in the named sets and the tool names they provide.
func Build(sets []string, cfg Config) ([]agent.Tool, []string, error) {
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = 60 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}

	var ws *workspace
	needWorkspace := false
	for _, s := range sets {
		if s == "files" || s == "shell" {
			needWorkspace = true
		}
	}
	if needWorkspace {
		var err error
		if ws, err = newWorkspace(cfg.Workspace); err != nil {
			return nil, nil, err
		}
	}

	var out []agent.Tool
	for _, s := range sets {
		switch s {
		case "files":
			out = append(out, listFiles{ws}, readFile{ws}, writeFile{ws})
		case "shell":
			out = append(out, runCommand{ws, cfg.CommandTimeout})
		case "web":
			out = append(out, fetchURL{cfg.HTTPClient})
		default:
			return nil, nil, fmt.Errorf("unknown tool set %q (choose from %s)", s, strings.Join(Sets, ", "))
		}
	}
	names := make([]string, 0, len(out))
	for _, t := range out {
		names = append(names, t.Spec().Function.Name)
	}
	return out, names, nil
}

func spec(name, desc string, params map[string]string, required ...string) agent.ToolSpec {
	return agent.ToolSpec{Type: "function", Function: agent.FunctionSpec{
		Name: name, Description: desc, Parameters: agent.ParamSchema(params, required...),
	}}
}

func stringArg(args map[string]any, name string) (string, error) {
	v, ok := args[name]
	if !ok {
		return "", fmt.Errorf("missing argument %q", name)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", name)
	}
	return s, nil
}

// workspace resolves paths inside a root directory.
type workspace struct {
	root string // absolute, symlinks resolved
}

func newWorkspace(dir string) (*workspace, error) {
	if dir == "" {
		return nil, errors.New("tools need a workspace directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	return &workspace{root: root}, nil
}

// resolve maps a workspace-relative path to an absolute one, refusing anything that
// escapes the workspace, including through symlinks.
func (w *workspace) resolve(rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the workspace", rel)
	}
	p := filepath.Join(w.root, filepath.Clean("/"+rel))

	// Resolve symlinks on the longest part of the path that exists.
	existing := p
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if real != w.root && !strings.HasPrefix(real, w.root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace", rel)
	}
	return p, nil
}

type listFiles struct{ ws *workspace }

func (listFiles) Spec() agent.ToolSpec {
	return spec("list_files", "List files and directories under a path in the workspace, recursively (up to 500 entries).",
		map[string]string{"path": "Directory relative to the workspace; use \".\" for the top."})
}

func (t listFiles) Call(ctx context.Context, args map[string]any) (string, error) {
	rel, _ := args["path"].(string)
	if rel == "" {
		rel = "."
	}
	dir, err := t.ws.resolve(rel)
	if err != nil {
		return "", err
	}
	var entries []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		if len(entries) >= 500 {
			return fs.SkipAll
		}
		name, _ := filepath.Rel(t.ws.root, p)
		name = filepath.ToSlash(name)
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			name += "/"
		}
		entries = append(entries, name)
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "(empty)", nil
	}
	sort.Strings(entries)
	return strings.Join(entries, "\n"), nil
}

type readFile struct{ ws *workspace }

func (readFile) Spec() agent.ToolSpec {
	return spec("read_file", "Read a text file from the workspace.",
		map[string]string{"path": "File path relative to the workspace."}, "path")
}

func (t readFile) Call(ctx context.Context, args map[string]any) (string, error) {
	rel, err := stringArg(args, "path")
	if err != nil {
		return "", err
	}
	p, err := t.ws.resolve(rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type writeFile struct{ ws *workspace }

func (writeFile) Spec() agent.ToolSpec {
	return spec("write_file", "Create or overwrite a text file in the workspace, creating directories as needed.",
		map[string]string{
			"path":    "File path relative to the workspace.",
			"content": "The full contents to write.",
		}, "path", "content")
}

func (t writeFile) Call(ctx context.Context, args map[string]any) (string, error) {
	rel, err := stringArg(args, "path")
	if err != nil {
		return "", err
	}
	content, err := stringArg(args, "content")
	if err != nil {
		return "", err
	}
	p, err := t.ws.resolve(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), rel), nil
}

type runCommand struct {
	ws      *workspace
	timeout time.Duration
}

func (t runCommand) Spec() agent.ToolSpec {
	return spec("run_command",
		fmt.Sprintf("Run a shell command in the workspace directory and return its output and exit code. Commands are killed after %s.", t.timeout),
		map[string]string{"command": "The shell command to run."}, "command")
}

func (t runCommand) Call(ctx context.Context, args map[string]any) (string, error) {
	command, err := stringArg(args, "command")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = t.ws.root
	// Child processes can hold the output pipe open after the shell is killed; stop
	// waiting for them shortly after the timeout.
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()

	result := strings.TrimRight(string(out), "\n")
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return result + fmt.Sprintf("\n[killed after %s]", t.timeout), nil
	case err != nil:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return result + fmt.Sprintf("\n[exit code %d]", exitErr.ExitCode()), nil
		}
		return "", err
	}
	return result + "\n[exit code 0]", nil
}

type fetchURL struct{ client *http.Client }

func (fetchURL) Spec() agent.ToolSpec {
	return spec("fetch_url", "Fetch a web page or file over HTTP(S) and return its text (HTML tags are stripped).",
		map[string]string{"url": "The http:// or https:// URL to fetch."}, "url")
}

var (
	scriptOrStyle = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	htmlTag       = regexp.MustCompile(`(?s)<[^>]+>`)
	blankLines    = regexp.MustCompile(`\n\s*\n+`)
)

func (t fetchURL) Call(ctx context.Context, args map[string]any) (string, error) {
	url, err := stringArg(args, "url")
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", errors.New("only http:// and https:// URLs are allowed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	text := string(data)
	if strings.Contains(resp.Header.Get("Content-Type"), "html") {
		text = scriptOrStyle.ReplaceAllString(text, "")
		text = htmlTag.ReplaceAllString(text, "")
		text = blankLines.ReplaceAllString(text, "\n\n")
	}
	return fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, strings.TrimSpace(text)), nil
}
