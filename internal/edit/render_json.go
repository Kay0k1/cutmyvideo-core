package edit

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Omission selects the documented zero offset. An explicit null is not a
// numeric offset: in particular, JSON encoders must not turn NaN into a
// silently accepted synchronization change.
func (r *AudioReplace) UnmarshalJSON(data []byte) error {
	type value AudioReplace
	var decoded value
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var raw struct {
		Offset json.RawMessage `json:"offset_ms"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(raw.Offset), []byte("null")) {
		return errors.New("audio_replace.offset_ms must be an integer when supplied")
	}
	*r = AudioReplace(decoded)
	return nil
}
