package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/skinnyrad/agent-cluster/internal/agent"
)

func build(t *testing.T, sets ...string) (map[string]agent.Tool, string) {
	t.Helper()
	dir := t.TempDir()
	list, names, err := Build(sets, Config{Workspace: dir, CommandTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != len(names) {
		t.Fatalf("got %d tools but %d names", len(list), len(names))
	}
	byName := map[string]agent.Tool{}
	for _, tool := range list {
		byName[tool.Spec().Function.Name] = tool
	}
	return byName, dir
}

func call(t *testing.T, tool agent.Tool, args map[string]any) (string, error) {
	t.Helper()
	return tool.Call(context.Background(), args)
}

func TestFileTools(t *testing.T) {
	tools, _ := build(t, "files")

	if _, err := call(t, tools["write_file"], map[string]any{"path": "src/main.go", "content": "package main\n"}); err != nil {
		t.Fatal(err)
	}
	got, err := call(t, tools["read_file"], map[string]any{"path": "src/main.go"})
	if err != nil || got != "package main\n" {
		t.Fatalf("read_file = %q, %v", got, err)
	}
	list, err := call(t, tools["list_files"], map[string]any{"path": "."})
	if err != nil || list != "src/\nsrc/main.go" {
		t.Fatalf("list_files = %q, %v", list, err)
	}
}

func TestFileToolsStayInWorkspace(t *testing.T) {
	tools, dir := build(t, "files")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}

	for _, path := range []string{"link/secret", filepath.Join(outside, "secret")} {
		if _, err := call(t, tools["read_file"], map[string]any{"path": path}); err == nil {
			t.Errorf("read_file(%q) succeeded, want refusal", path)
		}
	}
	if _, err := call(t, tools["write_file"], map[string]any{"path": "link/new", "content": "x"}); err == nil {
		t.Error("write_file through a symlink out of the workspace succeeded")
	}

	// ".." is cleaned to stay inside the workspace rather than escaping it.
	if _, err := call(t, tools["write_file"], map[string]any{"path": "../../escape.txt", "content": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err != nil {
		t.Errorf("expected ../../escape.txt to land inside the workspace: %v", err)
	}
}

func TestRunCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh syntax")
	}
	tools, _ := build(t, "shell")

	out, err := call(t, tools["run_command"], map[string]any{"command": "echo hi > f.txt && cat f.txt"})
	if err != nil || out != "hi\n[exit code 0]" {
		t.Fatalf("run_command = %q, %v", out, err)
	}
	out, _ = call(t, tools["run_command"], map[string]any{"command": "exit 3"})
	if !strings.Contains(out, "[exit code 3]") {
		t.Errorf("failing command output = %q", out)
	}
	start := time.Now()
	out, _ = call(t, tools["run_command"], map[string]any{"command": "true && sleep 30"})
	if !strings.Contains(out, "[killed after 2s]") {
		t.Errorf("slow command output = %q", out)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("slow command took %s to return", elapsed)
	}
}

func TestFetchURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><script>x()</script><p>Hello <b>web</b></p></html>"))
	}))
	defer srv.Close()
	tools, _ := build(t, "web")

	out, err := call(t, tools["fetch_url"], map[string]any{"url": srv.URL})
	if err != nil || out != "HTTP 200\nHello web" {
		t.Fatalf("fetch_url = %q, %v", out, err)
	}
	if _, err := call(t, tools["fetch_url"], map[string]any{"url": "file:///etc/passwd"}); err == nil {
		t.Error("fetch_url accepted a file:// URL")
	}
}

func TestUnknownSet(t *testing.T) {
	if _, _, err := Build([]string{"nope"}, Config{Workspace: t.TempDir()}); err == nil {
		t.Fatal("expected an error for an unknown tool set")
	}
}
