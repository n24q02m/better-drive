package scheduler

import "fmt"

func renderLinux(d Definition) []byte {
	name := ManagedName(d.JobID)
	return []byte(fmt.Sprintf(`# X-BetterDrive-Definition=%s
[Unit]
Description=better-drive managed job %s

[Service]
Type=oneshot
ExecStart=%s

[Timer]
OnUnitActiveSec=%s
Persistent=true
Unit=%s.service
`, definitionMetadata(d), name, commandLine(d), formatInterval(d.IntervalSeconds), name))
}
