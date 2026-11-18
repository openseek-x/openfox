package cliagent

import (
	"bytes"
	"encoding/json"
)

// toolArguments preserves tool-supplied numbers for the display-only preview.
// Scope UseNumber to arguments: usage counters and protocol/error metadata keep
// their existing decoding and validation behavior.
type toolArguments map[string]any

func (args *toolArguments) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*args = value
	return nil
}
