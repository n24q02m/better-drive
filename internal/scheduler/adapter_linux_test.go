//go:build linux

package scheduler

import (
	"strings"
	"testing"
)

func TestRenderLinuxUnitsOwnOneExactJobWithoutRawSelectorInjection(t *testing.T) {
	definition := testDefinition()
	definition.JobID = "job with spaces"
	service, timer := renderLinuxUnits(definition)
	serviceText, timerText := string(service), string(timer)
	name := ManagedName(definition.JobID)
	for _, want := range []string{"ExecStart=", `"--job" "job with spaces"`, "TimeoutStartSec=21600", "Description=better-drive managed job " + name} {
		if !strings.Contains(serviceText, want) {
			t.Errorf("service missing %q: %s", want, serviceText)
		}
	}
	for _, want := range []string{"Persistent=true", "Unit=" + name + ".service", "Description=better-drive schedule " + name} {
		if !strings.Contains(timerText, want) {
			t.Errorf("timer missing %q: %s", want, timerText)
		}
	}
	if strings.Contains(serviceText, "Description=better-drive managed job "+definition.JobID) || strings.Contains(timerText, "Description=better-drive schedule "+definition.JobID) {
		t.Fatal("raw job ID reached a systemd Description field")
	}
}

func TestLinuxUnitMissingRecognizesSystemctlNotFoundReadback(t *testing.T) {
	for _, output := range []string{
		"LoadState=not-found\n",
		"Unit better-drive.timer could not be found.\n",
		"Unit better-drive.timer not loaded.\n",
	} {
		if !linuxUnitMissing([]byte(output)) {
			t.Fatalf("linuxUnitMissing(%q) = false", output)
		}
	}
	if linuxUnitMissing([]byte("Failed to connect to bus")) {
		t.Fatal("systemd bus failure was treated as a missing unit")
	}
}
