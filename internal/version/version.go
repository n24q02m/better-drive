// Package version holds build metadata injected via -ldflags at release time.
package version

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)
