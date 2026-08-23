package scheduler

import (
	"fmt"
	"strings"
)

func renderWindows(d Definition) []byte {
	wake := "false"
	if d.CatchUp {
		wake = "true"
	}
	args := append([]string{"sync", "--format", "json", "--config", d.Config}, d.Arguments...)
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><URI>\%s</URI></RegistrationInfo>
  <Triggers><CalendarTrigger><Repetition><Interval>PT%dS</Interval></Repetition><Enabled>true</Enabled><StartBoundary>2000-01-01T00:00:00</StartBoundary></CalendarTrigger></Triggers>
  <Principals><Principal id="Author"><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings><MultipleInstancesPolicy>StopExisting</MultipleInstancesPolicy><WakeToRun>%s</WakeToRun><ExecutionTimeLimit>PT%dS</ExecutionTimeLimit><StartWhenAvailable>true</StartWhenAvailable></Settings>
  <Actions Context="Author"><Exec><Command>%s</Command><Arguments>%s</Arguments></Exec></Actions>
</Task>
`, xmlEscape(d.JobID), d.IntervalSeconds, wake, d.ExecutionLimitSeconds, xmlEscape(d.Executable), xmlEscape(strings.Join(quoteArgs(args), " "))))
}

func xmlEscape(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	return value
}
