package main

import (
	"strings"
	"testing"
)

func TestExtractCodeChanges_Write(t *testing.T) {
	reply := "```src/main.go\npackage main\n\nfunc main() {}\n```"
	changes := ExtractCodeChanges(reply)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	ch := changes[0]
	if ch.Path != "/src/main.go" {
		t.Errorf("path: got %q, want %q", ch.Path, "/src/main.go")
	}
	if ch.Action != "write" {
		t.Errorf("action: got %q, want write", ch.Action)
	}
	if !strings.Contains(ch.Content, "func main()") {
		t.Errorf("content missing func main()")
	}
}

func TestExtractCodeChanges_Replace(t *testing.T) {
	reply := "```src/main.go\n<<<<<<< SEARCH\nfunc main() {}\n=======\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n>>>>>>> REPLACE\n```"
	changes := ExtractCodeChanges(reply)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	ch := changes[0]
	if ch.Action != "replace" {
		t.Errorf("action: got %q, want replace", ch.Action)
	}
	if ch.Old != "func main() {}" {
		t.Errorf("old: got %q", ch.Old)
	}
	if !strings.Contains(ch.New, "fmt.Println") {
		t.Errorf("new missing fmt.Println")
	}
}

func TestExtractCodeChanges_Diff(t *testing.T) {
	reply := "```diff\n--- a/src/main.go\n+++ b/src/main.go\n@@ -1 +1 @@\n-old\n+new\n```"
	changes := ExtractCodeChanges(reply)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	ch := changes[0]
	if ch.Action != "diff" {
		t.Errorf("action: got %q, want diff", ch.Action)
	}
	if ch.Path != "/src/main.go" {
		t.Errorf("path: got %q", ch.Path)
	}
}

func TestExtractShellCommands(t *testing.T) {
	reply := "Run this:\n```bash\ngo test ./...\n```\nAnd:\n```sh\nls -la\n```"
	cmds := ExtractShellCommands(reply)
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}
	if cmds[0] != "go test ./..." {
		t.Errorf("cmd0: got %q", cmds[0])
	}
	if cmds[1] != "ls -la" {
		t.Errorf("cmd1: got %q", cmds[1])
	}
}

func TestApplyUnifiedDiff(t *testing.T) {
	original := "line1\nline2\nline3\nline4\n"
	diff := "--- a/f.go\n+++ b/f.go\n@@ -1,4 +1,4 @@\n line1\n-line2\n+line2changed\n line3\n line4\n"
	result, err := applyUnifiedDiff(original, diff)
	if err != nil {
		t.Fatalf("applyUnifiedDiff error: %v", err)
	}
	expected := "line1\nline2changed\nline3\nline4\n"
	if result != expected {
		t.Errorf("got:\n%s\nwant:\n%s", result, expected)
	}
}
