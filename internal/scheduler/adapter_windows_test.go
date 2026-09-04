//go:build windows

package scheduler

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestWindowsXMLUsesUTF16LEWithBOM(t *testing.T) {
	source := renderWindows(testDefinition())
	encoded := windowsXML(source)
	if len(encoded) < 4 || encoded[0] != 0xff || encoded[1] != 0xfe {
		t.Fatalf("Windows task XML prefix = %x, want UTF-16LE BOM", encoded[:min(len(encoded), 4)])
	}
	units := make([]uint16, (len(encoded)-2)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(encoded[2+index*2:])
	}
	decoded := string(utf16.Decode(units))
	if decoded != string(source) || !strings.Contains(decoded, `encoding="UTF-16"`) {
		t.Fatalf("Windows task XML encoding round trip mismatch")
	}
}
