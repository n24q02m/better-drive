package cleanup

import "testing"

func TestDecodeStrictJSONRecordRejectsUnknownAndTrailingData(t *testing.T) {
	var output struct {
		Value string `json:"value"`
	}
	if err := decodeStrictJSONRecord([]byte(`{"value":"ok"}`), &output); err != nil {
		t.Fatal(err)
	}
	if output.Value != "ok" {
		t.Fatalf("decoded value = %q", output.Value)
	}
	if err := decodeStrictJSONRecord([]byte(`{"value":"ok","unknown":true}`), &output); err == nil {
		t.Fatal("accepted unknown protected record field")
	}
	if err := decodeStrictJSONRecord([]byte(`{"value":"ok"} {}`), &output); err == nil {
		t.Fatal("accepted trailing protected record JSON")
	}
}
