package app

import (
	"encoding/json"
	"strings"
)

// Preserve retired claim flags on disk for rollback without exposing or using them.
func (m *Mailbox) captureRetiredClaims(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	m.RetiredClaims = map[string]bool{}
	for key, raw := range fields {
		if key == "openai_claimed" || !strings.HasSuffix(key, "_claimed") {
			continue
		}
		var claimed bool
		if err := json.Unmarshal(raw, &claimed); err != nil {
			return err
		}
		m.RetiredClaims[key] = claimed
	}
	return nil
}

func (m Mailbox) MarshalJSON() ([]byte, error) {
	type plain Mailbox
	raw, err := json.Marshal(plain(m))
	if err != nil || len(m.RetiredClaims) == 0 {
		return raw, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for key, claimed := range m.RetiredClaims {
		if key == "openai_claimed" || !strings.HasSuffix(key, "_claimed") {
			continue
		}
		fields[key], _ = json.Marshal(claimed)
	}
	return json.Marshal(fields)
}
