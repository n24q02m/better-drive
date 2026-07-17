package config

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func TranslateDriveIgnore(localRoot string) ([]string, error) {
	f, err := os.Open(filepath.Join(localRoot, ".driveignore"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sign := "- "
		if strings.HasPrefix(line, "!") {
			sign = "+ "
			line = line[1:]
		}
		out = append(out, sign+toRclonePattern(line))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// gitignore: last-match-wins. rclone filter: first-match-wins. Reverse so
	// later negations (which must win) are checked before the rules they negate.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func toRclonePattern(pat string) string {
	dir := strings.HasSuffix(pat, "/")
	trimmed := strings.TrimSuffix(pat, "/")
	// gitignore: a "/" at the start or middle anchors the pattern to root; a
	// slash-less pattern (or one with only a trailing "/") matches at any depth.
	// rclone uses the same leading-"/" = "^" convention and auto-prefixes "(^|/)"
	// for un-anchored patterns, so DO NOT add "**/".
	anchored := strings.Contains(trimmed, "/")
	body := strings.TrimPrefix(trimmed, "/")
	if dir {
		body += "/**"
	}
	if anchored {
		body = "/" + body
	}
	return body
}
