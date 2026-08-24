package scheduler

import "fmt"

func renderDarwin(d Definition) []byte {
	args := append([]string{d.Executable}, schedulerArguments(d)...)
	items := ""
	for _, arg := range args {
		items += fmt.Sprintf("    <string>%s</string>\n", xmlEscape(arg))
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.better-drive.LaunchAgent.%s</string>
<key>ProgramArguments</key><array>
%s</array>
<key>StartInterval</key><integer>%d</integer>
<key>RunAtLoad</key><true/>
</dict></plist>
`, xmlEscape(d.JobID), items, d.IntervalSeconds))
}
