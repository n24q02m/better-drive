package cleanup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func decodeStrictJSONRecord(data []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON is not allowed")
	}
	return nil
}
