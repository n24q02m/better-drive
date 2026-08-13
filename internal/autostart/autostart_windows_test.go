//go:build windows

package autostart

import (
	"errors"
	"testing"
)

type memoryRunRegistry struct {
	value  string
	exists bool
	err    error
}

func (r *memoryRunRegistry) set(value string) error {
	if r.err != nil {
		return r.err
	}
	r.value = value
	r.exists = true
	return nil
}

func (r *memoryRunRegistry) delete() error {
	if r.err != nil {
		return r.err
	}
	r.value = ""
	r.exists = false
	return nil
}

func (r *memoryRunRegistry) enabled() (bool, error) {
	if r.err != nil {
		return false, r.err
	}
	return r.exists, nil
}

// TestAutostartRoundTrip exercises the complete enable/read/disable contract
// against an in-memory registry. Unit tests must never overwrite or delete the
// user's production HKCU Run registration.
func TestAutostartRoundTrip(t *testing.T) {
	store := &memoryRunRegistry{}
	a := newAutostart(store)

	if err := a.enable(`C:\tmp\better-drive.exe`); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if want := `"C:\tmp\better-drive.exe" run`; store.value != want {
		t.Fatalf("value = %q, want %q", store.value, want)
	}
	on, err := a.enabled()
	if err != nil || !on {
		t.Fatalf("enabled after enable = %v, %v; want true, nil", on, err)
	}

	if err := a.disable(); err != nil {
		t.Fatalf("disable: %v", err)
	}
	on, err = a.enabled()
	if err != nil || on {
		t.Fatalf("enabled after disable = %v, %v; want false, nil", on, err)
	}
}

func TestAutostartPropagatesRegistryErrors(t *testing.T) {
	want := errors.New("registry unavailable")
	a := newAutostart(&memoryRunRegistry{err: want})

	if err := a.enable(`C:\tmp\better-drive.exe`); !errors.Is(err, want) {
		t.Fatalf("enable error = %v, want %v", err, want)
	}
	if err := a.disable(); !errors.Is(err, want) {
		t.Fatalf("disable error = %v, want %v", err, want)
	}
	if _, err := a.enabled(); !errors.Is(err, want) {
		t.Fatalf("enabled error = %v, want %v", err, want)
	}
}
