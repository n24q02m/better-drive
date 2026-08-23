package scheduler

import (
	"fmt"
	"strings"
)

func renderLinux(d Definition) []byte {
	return []byte(fmt.Sprintf(`[Unit]
Description=better-drive job %s

[Service]
Type=oneshot
ExecStart=%s

[Timer]
OnUnitActiveSec=%s
Persistent=true
Unit=better-drive-%s.service
`, d.JobID, commandLine(d), formatInterval(d.IntervalSeconds), strings.ReplaceAll(d.JobID, " ", "-")))
}
