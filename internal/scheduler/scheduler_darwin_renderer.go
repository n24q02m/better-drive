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
<key>Label</key><string>%s</string>
<key>BetterDriveDefinition</key><string>%s</string>
<key>ProgramArguments</key><array>
%s</array>
<key>StartInterval</key><integer>%d</integer>
<key>RunAtLoad</key><false/>
<key>ProcessType</key><string>Background</string>
</dict></plist>
`, darwinLabel(d.JobID), xmlEscape(definitionMetadata(d)), items, d.IntervalSeconds))
}
