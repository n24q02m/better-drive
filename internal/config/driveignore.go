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
	return out, nil
}

func toRclonePattern(pat string) string {
	anchored := strings.HasPrefix(pat, "/")
	dir := strings.HasSuffix(pat, "/")
	pat = strings.TrimPrefix(pat, "/")
	pat = strings.TrimSuffix(pat, "/")
	if dir {
		pat += "/**"
	}
	// gitignore: pattern without "/" matches at any level; rooted patterns don't get **/
	if !anchored && !strings.Contains(strings.TrimSuffix(pat, "/**"), "/") {
		pat = "**/" + pat
	}
	return pat
}
