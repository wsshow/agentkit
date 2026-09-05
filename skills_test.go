package agentkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestFileSkillBackendLoadsPathsAndReloads(t *testing.T) {
	root := t.TempDir()
	alphaPath := writeSkillFile(t, filepath.Join(root, "alpha"), "alpha", "Alpha skill", "first instructions", "\n")
	writeSkillFile(t, filepath.Join(root, "beta"), "beta", "Beta skill", "second instructions", "\r\n")
	writeSkillFile(t, filepath.Join(root, "deep", "nested"), "ignored", "Nested skill", "ignored", "\n")

	backend, err := NewFileSkillBackend(root, alphaPath)
	if err != nil {
		t.Fatalf("NewFileSkillBackend() error = %v", err)
	}

	infos, err := backend.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(infos) != 2 || infos[0].Name != "alpha" || infos[1].Name != "beta" {
		t.Fatalf("List() = %#v, want alpha and beta", infos)
	}

	alpha, err := backend.Get(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Get(alpha) error = %v", err)
	}
	if alpha.Content != "first instructions" {
		t.Fatalf("Get(alpha).Content = %q", alpha.Content)
	}
	wantBase, err := filepath.Abs(filepath.Join(root, "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if alpha.BaseDirectory != wantBase {
		t.Fatalf("Get(alpha).BaseDirectory = %q, want %q", alpha.BaseDirectory, wantBase)
	}

	writeSkillFile(t, filepath.Join(root, "alpha"), "alpha", "Alpha skill", "updated instructions", "\n")
	alpha, err = backend.Get(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Get(alpha) after update error = %v", err)
	}
	if alpha.Content != "updated instructions" {
		t.Fatalf("Get(alpha).Content after update = %q", alpha.Content)
	}

	_, err = backend.Get(context.Background(), "missing")
	if !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrSkillNotFound", err)
	}
}

func TestFileSkillBackendTreatsDirectoryWithSkillAsSingleSkill(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "root", "Root skill", "root instructions", "\n")
	writeSkillFile(t, filepath.Join(root, "nested"), "nested", "Nested skill", "nested instructions", "\n")
	backend, err := NewFileSkillBackend(root)
	if err != nil {
		t.Fatal(err)
	}
	infos, err := backend.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name != "root" {
		t.Fatalf("List() = %#v, want only root skill", infos)
	}
}

func TestFileSkillBackendRejectsInvalidSkills(t *testing.T) {
	t.Run("duplicate names", func(t *testing.T) {
		root := t.TempDir()
		writeSkillFile(t, filepath.Join(root, "one"), "same", "First", "one", "\n")
		writeSkillFile(t, filepath.Join(root, "two"), "same", "Second", "two", "\n")
		backend, err := NewFileSkillBackend(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := backend.List(context.Background()); err == nil || !strings.Contains(err.Error(), "duplicate skill name") {
			t.Fatalf("List() error = %v, want duplicate skill name", err)
		}
	})

	tests := []struct {
		name     string
		document string
		want     string
	}{
		{name: "missing frontmatter", document: "instructions", want: "must start"},
		{name: "missing closing delimiter", document: "---\nname: demo\n", want: "closing delimiter"},
		{name: "missing name", document: "---\ndescription: Demo\n---\ninstructions", want: "skill name is required"},
		{name: "missing description", document: "---\nname: demo\n---\ninstructions", want: "skill description is required"},
		{name: "missing instructions", document: "---\nname: demo\ndescription: Demo\n---\n", want: "skill instructions are required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), skillFileName)
			if err := os.WriteFile(path, []byte(tt.document), 0o600); err != nil {
				t.Fatal(err)
			}
			backend, err := NewFileSkillBackend(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := backend.List(context.Background()); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("List() error = %v, want containing %q", err, tt.want)
			}
		})
	}

	t.Run("oversized file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), skillFileName)
		if err := os.WriteFile(path, []byte(strings.Repeat("x", defaultMaxSkillFileSize+1)), 0o600); err != nil {
			t.Fatal(err)
		}
		backend, err := NewFileSkillBackend(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := backend.List(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("List() error = %v, want file size error", err)
		}
	})
}

func TestFileSkillBackendHonorsContextCancellation(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "demo", "Demo", "instructions", "\n")
	backend, err := NewFileSkillBackend(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := backend.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("List() error = %v, want context.Canceled", err)
	}
}

func TestMemorySkillBackendManagesSkills(t *testing.T) {
	var backend MemorySkillBackend
	for _, item := range []Skill{
		{FrontMatter: SkillInfo{Name: "beta", Description: "Beta"}, Content: "second"},
		{FrontMatter: SkillInfo{Name: "alpha", Description: "Alpha"}, Content: "first"},
	} {
		if err := backend.Set(item); err != nil {
			t.Fatalf("Set() error = %v", err)
		}
	}
	infos, err := backend.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 || infos[0].Name != "alpha" || infos[1].Name != "beta" {
		t.Fatalf("List() = %#v, want alpha and beta", infos)
	}
	item, err := backend.Get(context.Background(), "alpha")
	if err != nil || item.Content != "first" {
		t.Fatalf("Get(alpha) = %#v, %v", item, err)
	}
	if err := backend.Delete("alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Get(context.Background(), "alpha"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrSkillNotFound", err)
	}
}

func TestMemorySkillBackendConcurrentAccess(t *testing.T) {
	var backend MemorySkillBackend
	var wait sync.WaitGroup
	for i := 0; i < 32; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			name := fmt.Sprintf("skill-%02d", i)
			if err := backend.Set(Skill{
				FrontMatter: SkillInfo{Name: name, Description: name},
				Content:     "instructions",
			}); err != nil {
				t.Errorf("Set(%s) error = %v", name, err)
				return
			}
			if _, err := backend.List(context.Background()); err != nil {
				t.Errorf("List() error = %v", err)
			}
			if _, err := backend.Get(context.Background(), name); err != nil {
				t.Errorf("Get(%s) error = %v", name, err)
			}
		}(i)
	}
	wait.Wait()
	infos, err := backend.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 32 {
		t.Fatalf("List() length = %d, want 32", len(infos))
	}
}

func TestAgentLoadsSkillThroughGeneratedTool(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, filepath.Join(root, "concise-answer"), "concise-answer", "Answer concisely", "Use no more than three sentences.", "\n")
	const callID = "load-skill-call"
	model := NewMockChatModel(
		MockModelToolCallWithID(callID, "load_skill", `{"skill":"concise-answer"}`),
		MockModelTextAfterToolResult(callID),
	)
	agent, err := New(context.Background(), &Config{
		Name:  "assistant",
		Model: model,
		Skills: &SkillsConfig{
			Paths:    []string{root},
			ToolName: "load_skill",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer agent.Close()
	events := newMockEventRecorder()
	agent.Subscribe(events.Record)

	if err := agent.Prompt(context.Background(), "Use the concise skill"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	end := events.Last(EventToolEnd)
	if end == nil || end.ToolName != "load_skill" || end.ToolCallID != callID {
		t.Fatalf("tool end event = %#v", end)
	}
	if !strings.Contains(end.Content, "Use no more than three sentences.") || !strings.Contains(end.Content, filepath.Join(root, "concise-answer")) {
		t.Fatalf("tool result = %q", end.Content)
	}
	if got := agent.State().Messages(); len(got) == 0 || !strings.Contains(got[len(got)-1].Content, "Use no more than three sentences.") {
		t.Fatalf("final messages = %#v", got)
	}
}

func TestAgentRejectsUnsupportedSkillExecutionMode(t *testing.T) {
	backend, err := NewMemorySkillBackend(Skill{
		FrontMatter: SkillInfo{Name: "forked", Description: "Forked", Context: "fork"},
		Content:     "instructions",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(context.Background(), &Config{
		Model:  NewMockChatModel(),
		Skills: &SkillsConfig{Backend: backend},
	})
	if err == nil || !strings.Contains(err.Error(), "requests fork, agent, or model overrides") {
		t.Fatalf("New() error = %v, want unsupported execution mode", err)
	}
}

func TestAgentRejectsEmptySkillBackend(t *testing.T) {
	_, err := New(context.Background(), &Config{
		Model:  NewMockChatModel(),
		Skills: &SkillsConfig{Backend: &MemorySkillBackend{}},
	})
	if err == nil || !strings.Contains(err.Error(), "no skills found") {
		t.Fatalf("New() error = %v, want no skills found", err)
	}
}

func TestAgentIsolatesSkillBackendListPanic(t *testing.T) {
	_, err := New(context.Background(), &Config{
		Model: NewMockChatModel(),
		Skills: &SkillsConfig{Backend: &panicSkillBackend{
			list: true,
		}},
	})
	if !errors.Is(err, ErrSkillBackendPanic) {
		t.Fatalf("New() error = %v, want ErrSkillBackendPanic", err)
	}
}

func TestAgentIsolatesSkillBackendGetPanic(t *testing.T) {
	const callID = "load-skill-panic"
	agent, err := New(context.Background(), &Config{
		Name:  "assistant",
		Model: NewMockChatModel(MockModelToolCallWithID(callID, "skill", `{"skill":"demo"}`)),
		Skills: &SkillsConfig{Backend: &panicSkillBackend{
			get: true,
		}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	if err := agent.Prompt(context.Background(), "load demo"); !errors.Is(err, ErrSkillBackendPanic) {
		t.Fatalf("Prompt() error = %v, want ErrSkillBackendPanic", err)
	}
}

type panicSkillBackend struct {
	list bool
	get  bool
}

func (b *panicSkillBackend) List(context.Context) ([]SkillInfo, error) {
	if b.list {
		panic("broken list backend")
	}
	return []SkillInfo{{Name: "demo", Description: "Demo"}}, nil
}

func (b *panicSkillBackend) Get(context.Context, string) (Skill, error) {
	if b.get {
		panic("broken get backend")
	}
	return Skill{FrontMatter: SkillInfo{Name: "demo", Description: "Demo"}, Content: "instructions"}, nil
}

func writeSkillFile(t *testing.T, dir, name, description, content, newline string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	document := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n%s\n", name, description, content)
	if newline != "\n" {
		document = strings.ReplaceAll(document, "\n", newline)
	}
	path := filepath.Join(dir, skillFileName)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
