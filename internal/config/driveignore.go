package config

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// readDriveIgnoreLines returns the raw (untrimmed, unfiltered) lines of the
// .driveignore file at localRoot, or (nil, nil) if the file does not exist.
func readDriveIgnoreLines(localRoot string) ([]string, error) {
	f, err := os.Open(filepath.Join(localRoot, ".driveignore"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// TranslateIgnoreLines converts a list of gitignore-syntax lines (blank lines
// and "#" comments are skipped) into rclone filter rules ("+ "/"- " prefixed
// patterns), ready for a bisync filters file or an rc _filter.FilterRule list.
//
// gitignore: last-match-wins. rclone filter: first-match-wins. The lines are
// reversed as a whole after translation so later negations (which must win
// under gitignore semantics) are checked before the rules they negate.
func TranslateIgnoreLines(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	// Pre-allocate the slice to avoid dynamic growth and reallocation overhead in tight loop
	out := make([]string, 0, len(lines))
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		// Use direct byte indexing instead of strings.HasPrefix to save function call overhead
		if line == "" || line[0] == '#' {
			continue
		}

		sign := "- "
		if line[0] == '!' {
			sign = "+ "
			line = line[1:]
		}

		if len(line) == 0 {
			continue
		}

		// Use direct indexing/slicing instead of strings.HasSuffix/strings.TrimSuffix
		dir := line[len(line)-1] == '/'

		body := line
		if dir {
			body = body[:len(body)-1]
		}

		anchored := strings.ContainsRune(body, '/')
		if len(body) > 0 && body[0] == '/' {
			body = body[1:]
		}

		// gitignore: a "/" at the start or middle anchors the pattern to root; a
		// slash-less pattern (or one with only a trailing "/") matches at any depth.
		// rclone uses the same leading-"/" = "^" convention and auto-prefixes "(^|/)"
		// for un-anchored patterns, so DO NOT add "**/".
		if dir {
			body += "/**"
		}
		if anchored {
			body = "/" + body
		}
		out = append(out, sign+body)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// TranslateDriveIgnore reads the .driveignore file at localRoot (if any) and
// translates it to rclone filter rules. Returns (nil, nil) when no
// .driveignore file exists.
func TranslateDriveIgnore(localRoot string) ([]string, error) {
	lines, err := readDriveIgnoreLines(localRoot)
	if err != nil {
		return nil, err
	}
	return TranslateIgnoreLines(lines), nil
}

// PairFilters combines a pair's config-level exclude patterns with any
// .driveignore file found at localRoot into a single translated rclone
// filter-rule list. exclude entries come first, then the .driveignore file's
// lines - so, under gitignore's last-match-wins semantics, a folder's own
// .driveignore can override a pattern set in the pair's config. This lets a
// pair (e.g. a real user directory like ~/.claude) be excluded entirely via
// config, with no .driveignore file ever written into that directory.
func PairFilters(localRoot string, exclude []string) ([]string, error) {
	fileLines, err := readDriveIgnoreLines(localRoot)
	if err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(exclude)+len(fileLines))
	lines = append(lines, exclude...)
	lines = append(lines, fileLines...)
	return TranslateIgnoreLines(lines), nil
}
