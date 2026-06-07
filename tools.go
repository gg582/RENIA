package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WorkspaceEngine performs file operations within a workspace boundary.
type WorkspaceEngine struct {
	db            *DB
	userID        int64
	sessionID     int64
	workspacePath string
}

func NewWorkspaceEngine(db *DB, userID, sessionID int64, workspacePath string) *WorkspaceEngine {
	return &WorkspaceEngine{
		db:            db,
		userID:        userID,
		sessionID:     sessionID,
		workspacePath: workspacePath,
	}
}

func (e *WorkspaceEngine) resolvePath(rel string) (string, error) {
	rel = filepath.Clean("/" + rel)
	abs := filepath.Join(e.workspacePath, rel)
	// Ensure the resolved path is still within workspace.
	wp := filepath.Clean(e.workspacePath)
	if !strings.HasPrefix(abs, wp) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return abs, nil
}

func (e *WorkspaceEngine) snapshot(relPath, action string) {
	abs, err := e.resolvePath(relPath)
	if err != nil {
		return
	}
	content, _ := os.ReadFile(abs)
	_ = e.db.appendFileSnapshot(context.Background(), e.sessionID, relPath, string(content), action)
}

func (e *WorkspaceEngine) readFile(relPath string) (string, error) {
	abs, err := e.resolvePath(relPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	_ = e.db.appendFileSnapshot(context.Background(), e.sessionID, relPath, string(data), "read")
	return string(data), nil
}

func (e *WorkspaceEngine) writeFile(relPath, content string) error {
	abs, err := e.resolvePath(relPath)
	if err != nil {
		return err
	}
	e.snapshot(relPath, "write")
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0644)
}

func (e *WorkspaceEngine) strReplaceFile(relPath, old, new string) error {
	abs, err := e.resolvePath(relPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	content := string(data)
	if !strings.Contains(content, old) {
		return fmt.Errorf("old string not found in file")
	}
	e.snapshot(relPath, "replace")
	content = strings.Replace(content, old, new, 1)
	return os.WriteFile(abs, []byte(content), 0644)
}

func (e *WorkspaceEngine) listDir(relPath string) (string, error) {
	abs, err := e.resolvePath(relPath)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, ent := range entries {
		prefix := "F"
		if ent.IsDir() {
			prefix = "D"
		}
		lines = append(lines, fmt.Sprintf("%s %s", prefix, ent.Name()))
	}
	return strings.Join(lines, "\n"), nil
}

func (e *WorkspaceEngine) executeCommand(command string) (string, error) {
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = e.workspacePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("ERROR: %v\nOUTPUT:\n%s", err, string(out)), nil
	}
	return string(out), nil
}

func (e *WorkspaceEngine) runTool(call toolCall) (string, error) {
	switch call.Tool {
	case "read_file":
		path, _ := call.Params["path"].(string)
		return e.readFile(path)
	case "write_file":
		path, _ := call.Params["path"].(string)
		content, _ := call.Params["content"].(string)
		return "", e.writeFile(path, content)
	case "str_replace_file":
		path, _ := call.Params["path"].(string)
		old, _ := call.Params["old"].(string)
		new, _ := call.Params["new"].(string)
		return "", e.strReplaceFile(path, old, new)
	case "list_dir":
		path, _ := call.Params["path"].(string)
		if path == "" {
			path = "."
		}
		return e.listDir(path)
	case "execute_command":
		command, _ := call.Params["command"].(string)
		return e.executeCommand(command)
	default:
		return "", fmt.Errorf("unknown workspace tool: %s", call.Tool)
	}
}

func (e *WorkspaceEngine) applyChange(change FileChange) (string, error) {
	switch change.Action {
	case "write":
		if err := e.writeFile(change.Path, change.Content); err != nil {
			return "", fmt.Errorf("write %s: %w", change.Path, err)
		}
		return fmt.Sprintf("Wrote %s", change.Path), nil
	case "replace":
		if err := e.strReplaceFile(change.Path, change.Old, change.New); err != nil {
			return "", fmt.Errorf("replace %s: %w", change.Path, err)
		}
		return fmt.Sprintf("Replaced in %s", change.Path), nil
	case "diff":
		abs, err := e.resolvePath(change.Path)
		if err != nil {
			return "", err
		}
		orig, _ := os.ReadFile(abs)
		patched, err := applyUnifiedDiff(string(orig), change.Diff)
		if err != nil {
			return "", fmt.Errorf("diff %s: %w", change.Path, err)
		}
		e.snapshot(change.Path, "diff")
		if err := os.WriteFile(abs, []byte(patched), 0644); err != nil {
			return "", fmt.Errorf("write %s: %w", change.Path, err)
		}
		return fmt.Sprintf("Applied diff to %s", change.Path), nil
	default:
		return "", fmt.Errorf("unknown change action: %s", change.Action)
	}
}

func (e *WorkspaceEngine) applyChanges(changes []FileChange) (string, error) {
	if len(changes) == 0 {
		return "", nil
	}
	var results []string
	for _, ch := range changes {
		res, err := e.applyChange(ch)
		if err != nil {
			results = append(results, fmt.Sprintf("FAIL %s: %v", ch.Path, err))
		} else {
			results = append(results, res)
		}
	}
	return strings.Join(results, "\n"), nil
}

func (e *WorkspaceEngine) runShellCommands(commands []string) (string, error) {
	if len(commands) == 0 {
		return "", nil
	}
	var results []string
	for _, cmd := range commands {
		out, err := e.executeCommand(cmd)
		if err != nil {
			results = append(results, fmt.Sprintf("$ %s\nERROR: %v\n%s", cmd, err, out))
		} else {
			results = append(results, fmt.Sprintf("$ %s\n%s", cmd, out))
		}
	}
	return strings.Join(results, "\n"), nil
}

func isFileTool(tool string) bool {
	switch tool {
	case "read_file", "write_file", "str_replace_file", "list_dir", "execute_command":
		return true
	}
	return false
}
