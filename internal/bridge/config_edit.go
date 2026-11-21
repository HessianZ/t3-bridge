package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

type configEdit struct {
	Path   []string        `json:"path"`
	Value  json.RawMessage `json:"value,omitempty"`
	Delete bool            `json:"delete,omitempty"`
}

// EditConfigJSON previews edits without writing. A candidate that fails final
// validation is returned with the error so the editor can keep the draft.
func EditConfigJSON(raw, edits []byte) ([]byte, error) {
	var config Config
	if err := decodeConfigObject(raw, &config); err != nil {
		return nil, err
	}
	var fields map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&fields); err != nil {
		return nil, errConfigJSON
	}
	if len(edits) != 0 {
		var changes []configEdit
		if !bytes.HasPrefix(bytes.TrimSpace(edits), []byte("[")) || decodeConfigJSON(edits, &changes) != nil {
			return nil, errors.New("config edits must be a JSON array")
		}
		for _, edit := range changes {
			if err := applyConfigEdit(fields, edit); err != nil {
				return nil, err
			}
		}
	}
	candidate, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return nil, err
	}
	candidate = append(candidate, '\n')
	_, err = ParseConfig(candidate)
	return candidate, err
}

func applyConfigEdit(fields map[string]any, edit configEdit) error {
	if len(edit.Path) == 0 || edit.Delete == (len(edit.Value) != 0) {
		return errors.New("each config edit requires a path and either value or delete")
	}
	if len(edit.Path) > 1 && edit.Path[0] == "additionalAccounts" {
		accounts, _ := fields["additionalAccounts"].([]any)
		index, err := strconv.Atoi(edit.Path[1])
		if err != nil || index < 0 || index >= len(accounts) {
			return errors.New("config edit account index is outside the existing account list")
		}
		if len(edit.Path) == 2 {
			if !edit.Delete {
				return errors.New("config edit must select an account field or delete the account")
			}
			fields["additionalAccounts"] = append(accounts[:index], accounts[index+1:]...)
			return nil
		}
		var ok bool
		fields, ok = accounts[index].(map[string]any)
		if !ok {
			return errors.New("config edit must select an account object")
		}
		edit.Path = edit.Path[2:]
	}
	for _, key := range edit.Path[:len(edit.Path)-1] {
		var ok bool
		fields, ok = fields[key].(map[string]any)
		if !ok {
			return errors.New("config edit path must lead through existing objects")
		}
	}
	key := edit.Path[len(edit.Path)-1]
	if edit.Delete {
		delete(fields, key)
		return nil
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(edit.Value))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return errors.New("invalid config edit value")
	}
	fields[key] = value
	return nil
}
