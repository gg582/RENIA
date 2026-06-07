package main

import (
	"bufio"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// FileChange represents a parsed code change from an AI reply.
type FileChange struct {
	Path    string `json:"path"`
	Action  string `json:"action"`  // "write", "replace", "diff"
	Content string `json:"content,omitempty"`
	Old     string `json:"old,omitempty"`
	New     string `json:"new,omitempty"`
	Diff    string `json:"diff,omitempty"`
}

// ExtractCodeChanges scans an AI reply for markdown code blocks that represent
// file modifications. Supported formats:
//
//  1. Full file write:
//     ```relative/path/to/file.go
//     package main
//     ...
//     ```
//
//  2. Search/Replace (aider-style):
//     ```relative/path/to/file.go
//     <<<<<<< SEARCH
//     old code
//     =======
//     new code
//     >>>>>>> REPLACE
//     ```
//
//  3. Unified diff:
//     ```diff
//     --- a/relative/path/to/file.go
//     +++ b/relative/path/to/file.go
//     @@ ...
//     ...
//     ```
func ExtractCodeChanges(reply string) []FileChange {
	blocks := extractMarkdownBlocks(reply)
	var changes []FileChange

	for _, blk := range blocks {
		switch {
		case blk.Lang == "diff":
			if ch := parseDiffBlock(blk.Content); ch != nil {
				changes = append(changes, *ch)
			}
		case isShellLang(blk.Lang):
			// Shell commands are handled by ExtractShellCommands.
			continue
		case looksLikeFilePath(blk.Lang):
			path := filepath.Clean("/" + blk.Lang)
			if ch := parseFileBlock(path, blk.Content); ch != nil {
				changes = append(changes, *ch)
			}
		}
	}

	return changes
}

// ExtractShellCommands scans an AI reply for shell code blocks.
// Recognises ```bash, ```sh, ```shell.
func ExtractShellCommands(reply string) []string {
	blocks := extractMarkdownBlocks(reply)
	var cmds []string
	for _, blk := range blocks {
		if isShellLang(blk.Lang) && strings.TrimSpace(blk.Content) != "" {
			cmds = append(cmds, strings.TrimSpace(blk.Content))
		}
	}
	return cmds
}

// ---------- internal helpers ----------

type codeBlock struct {
	Lang    string
	Content string
}

func extractMarkdownBlocks(reply string) []codeBlock {
	var blocks []codeBlock
	const fence = "```"

	for {
		start := strings.Index(reply, fence)
		if start == -1 {
			break
		}
		// Move past the opening fence
		afterOpen := reply[start+len(fence):]

		// Find the next newline to read the info string
		var info string
		if nl := strings.Index(afterOpen, "\n"); nl != -1 {
			info = strings.TrimSpace(afterOpen[:nl])
			afterOpen = afterOpen[nl+1:]
		}

		// Find closing fence
		end := strings.Index(afterOpen, fence)
		if end == -1 {
			break // unclosed block
		}

		content := afterOpen[:end]
		blocks = append(blocks, codeBlock{Lang: info, Content: content})

		// Advance past this block
		reply = afterOpen[end+len(fence):]
	}

	return blocks
}

func isShellLang(lang string) bool {
	switch lang {
	case "bash", "sh", "shell", "zsh", "cmd", "powershell", "ps1":
		return true
	}
	return false
}

func looksLikeFilePath(lang string) bool {
	if lang == "" {
		return false
	}
	// Disallow known non-file languages
	switch lang {
	case "go", "python", "py", "javascript", "js", "typescript", "ts",
		"java", "c", "cpp", "cxx", "h", "hpp", "rust", "rs",
		"html", "css", "json", "yaml", "yml", "toml", "xml",
		"sql", "md", "txt", "dockerfile", "makefile", "cmake",
		"templ", "proto", "graphql", "swift", "kt", "scala":
		// These are language tags, not paths. We require a "/" or a clear
		// path-like prefix for them to be treated as file paths.
		return strings.Contains(lang, "/") || strings.Contains(lang, ".")
	}
	// Anything else with a dot or slash is treated as a path
	return strings.Contains(lang, "/") || strings.Contains(lang, ".")
}

func parseFileBlock(path, content string) *FileChange {
	content = strings.TrimSuffix(content, "\n")
	// Check for search/replace markers
	const searchMarker = "<<<<<<< SEARCH"
	const sepMarker = "======="
	const replaceMarker = ">>>>>>> REPLACE"

	if strings.Contains(content, searchMarker) && strings.Contains(content, replaceMarker) {
		old, newText, ok := extractSearchReplace(content)
		if ok {
			return &FileChange{
				Path:   path,
				Action: "replace",
				Old:    old,
				New:    newText,
			}
		}
	}

	return &FileChange{
		Path:    path,
		Action:  "write",
		Content: content,
	}
}

func extractSearchReplace(content string) (old, newText string, ok bool) {
	const searchMarker = "<<<<<<< SEARCH"
	const sepMarker = "======="
	const replaceMarker = ">>>>>>> REPLACE"

	idxSearch := strings.Index(content, searchMarker)
	idxSep := strings.Index(content, sepMarker)
	idxReplace := strings.Index(content, replaceMarker)

	if idxSearch == -1 || idxSep == -1 || idxReplace == -1 {
		return "", "", false
	}
	if !(idxSearch < idxSep && idxSep < idxReplace) {
		return "", "", false
	}

	old = strings.TrimSuffix(content[idxSearch+len(searchMarker):idxSep], "\n")
	old = strings.TrimPrefix(old, "\n")
	newText = strings.TrimSuffix(content[idxSep+len(sepMarker):idxReplace], "\n")
	newText = strings.TrimPrefix(newText, "\n")
	return old, newText, true
}

func parseDiffBlock(content string) *FileChange {
	lines := strings.Split(content, "\n")
	var oldPath, newPath string
	for _, line := range lines {
		if strings.HasPrefix(line, "--- a/") {
			oldPath = strings.TrimPrefix(line, "--- a/")
		} else if strings.HasPrefix(line, "+++ b/") {
			newPath = strings.TrimPrefix(line, "+++ b/")
		}
	}
	path := newPath
	if path == "" {
		path = oldPath
	}
	if path == "" {
		return nil
	}
	return &FileChange{
		Path:   filepath.Clean("/" + path),
		Action: "diff",
		Diff:   content,
	}
}

// applyUnifiedDiff applies a unified diff to the given file content.
// It returns the patched content or an error.
func applyUnifiedDiff(original, diffText string) (string, error) {
	origLines := strings.Split(original, "\n")
	// Ensure origLines ends with an empty string only if original ends with newline
	if original != "" && !strings.HasSuffix(original, "\n") {
		// keep as-is
	} else if original == "" {
		origLines = []string{}
	}

	var result []string
	origIdx := 0

	scanner := bufio.NewScanner(strings.NewReader(diffText))
	inHunk := false
	var hunkOldStart int
	var hunkNewStart, hunkNewCount int
	var hunkOrigLines []string
	var hunkNewLines []string

	flushHunk := func() error {
		if !inHunk {
			return nil
		}
		// Verify we are at the right position in original
		if hunkOldStart > 0 {
			wantIdx := hunkOldStart - 1
			if origIdx != wantIdx {
				// Try to resync by searching for the context lines
				found := false
				for i := origIdx; i <= len(origLines) && i < wantIdx+10; i++ {
					if i == wantIdx {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("diff hunk offset mismatch: expected %d, at %d", wantIdx, origIdx)
				}
				for origIdx < wantIdx && origIdx < len(origLines) {
					result = append(result, origLines[origIdx])
					origIdx++
				}
			}
		}

		// Apply hunk
		for _, line := range hunkOrigLines {
			switch line[0] {
			case ' ':
				if origIdx >= len(origLines) {
					return fmt.Errorf("diff context line missing at %d", origIdx)
				}
				if origLines[origIdx] != line[1:] {
					// Fuzzy match: allow minor differences? For now strict.
					return fmt.Errorf("diff context mismatch at line %d: got %q, want %q", origIdx+1, origLines[origIdx], line[1:])
				}
				result = append(result, origLines[origIdx])
				origIdx++
			case '-':
				if origIdx >= len(origLines) {
					return fmt.Errorf("diff removal line missing at %d", origIdx)
				}
				if origLines[origIdx] != line[1:] {
					return fmt.Errorf("diff removal mismatch at line %d: got %q, want %q", origIdx+1, origLines[origIdx], line[1:])
				}
				origIdx++ // skip (remove)
			case '+':
				result = append(result, line[1:])
			}
		}
		_ = hunkNewStart
		_ = hunkNewCount
		_ = hunkNewLines
		inHunk = false
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "@@") {
			if err := flushHunk(); err != nil {
				return "", err
			}
			// Parse hunk header: @@ -oldStart,oldCount +newStart,newCount @@
			var err error
			hunkOldStart, _, hunkNewStart, hunkNewCount, err = parseHunkHeader(line)
			if err != nil {
				return "", err
			}
			hunkOrigLines = nil
			hunkNewLines = nil
			inHunk = true
			continue
		}
		if inHunk {
			if len(line) == 0 {
				// Empty line in hunk context — treat as context line
				hunkOrigLines = append(hunkOrigLines, " ")
			} else if line[0] == ' ' || line[0] == '+' || line[0] == '-' || line[0] == '\\' {
				if line[0] == '\\' {
					continue // ignore "\ No newline at end of file"
				}
				hunkOrigLines = append(hunkOrigLines, line)
				if line[0] == '+' {
					hunkNewLines = append(hunkNewLines, line[1:])
				}
			} else {
				// Hunk ended unexpectedly
				if err := flushHunk(); err != nil {
					return "", err
				}
			}
		}
	}
	if err := flushHunk(); err != nil {
		return "", err
	}

	// Append remaining original lines
	for origIdx < len(origLines) {
		result = append(result, origLines[origIdx])
		origIdx++
	}

	return strings.Join(result, "\n"), nil
}

func parseHunkHeader(line string) (oldStart, oldCount, newStart, newCount int, err error) {
	// Format: @@ -start,count +start,count @@
	parts := strings.Split(line, " ")
	if len(parts) < 4 {
		return 0, 0, 0, 0, fmt.Errorf("invalid hunk header: %s", line)
	}
	oldStart, oldCount, err = parseRange(parts[1])
	if err != nil {
		return 0, 0, 0, 0, err
	}
	newStart, newCount, err = parseRange(parts[2])
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return oldStart, oldCount, newStart, newCount, nil
}

func parseRange(s string) (start, count int, err error) {
	// s is like "-1,5" or "+1"
	s = strings.TrimPrefix(s, "-")
	s = strings.TrimPrefix(s, "+")
	if strings.Contains(s, ",") {
		parts := strings.SplitN(s, ",", 2)
		start, err = strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, err
		}
		count, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, err
		}
	} else {
		start, err = strconv.Atoi(s)
		if err != nil {
			return 0, 0, err
		}
		count = 1
	}
	if start == 0 && count == 0 {
		// New empty file
		start = 1
	}
	return start, count, nil
}
