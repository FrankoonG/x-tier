package configstore

import (
	"bytes"
	"encoding/json"
	"errors"
)

var errInvalidJSONStructure = errors.New("invalid JSON structure")

// preflightConfigJSON rejects ambiguous object members before a typed decoder
// can apply encoding/json's last-value-wins behavior.
func preflightConfigJSON(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return markContentError(err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return markContentError(err)
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}

	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errInvalidJSONStructure
			}
			if _, duplicate := seen[key]; duplicate {
				return configErrorf("config.json_duplicate_field")
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		return consumeJSONDelimiter(decoder, '}')
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		return consumeJSONDelimiter(decoder, ']')
	default:
		return errInvalidJSONStructure
	}
}

func consumeJSONDelimiter(decoder *json.Decoder, expected json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != expected {
		return errInvalidJSONStructure
	}
	return nil
}
