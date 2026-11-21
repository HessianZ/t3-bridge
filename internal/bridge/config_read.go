package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

const MaxConfigBytes = 1 << 20

var errConfigJSON = errors.New("invalid JSON config: syntax, fields, types or size")

// ReadConfigJSON reads a bounded JSON configuration file.
func ReadConfigJSON(path string) ([]byte, error) {
	return readConfigJSON(path, os.Open)
}

// ReadRegularConfigJSON reads bounded JSON from a regular file without following a link at its final component.
func ReadRegularConfigJSON(path string) ([]byte, error) {
	return readConfigJSON(path, localfile.OpenRegular)
}

// ReadPrivateConfigJSON requires a private regular file, as used by managed profiles and backups.
func ReadPrivateConfigJSON(path string) ([]byte, error) {
	return readConfigJSON(path, localfile.OpenPrivate)
}

func readConfigJSON(path string, open func(string) (*os.File, error)) ([]byte, error) {
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		return nil, errors.New("config must be JSON (.json)")
	}
	file, err := open(path)
	if err != nil {
		return nil, errors.New("cannot read config")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	if err != nil {
		return nil, errors.New("cannot read config")
	}
	if len(raw) > MaxConfigBytes {
		return nil, errors.New("config exceeds 1 MiB")
	}
	return raw, nil
}

func LoadConfig(path string) (Config, error) {
	raw, err := ReadConfigJSON(path)
	if err != nil {
		return Config{}, err
	}
	return ParseConfigAtPath(raw, path)
}

// ParseConfigAtPath associates a configuration snapshot with its original file for report commands.
func ParseConfigAtPath(raw []byte, path string) (Config, error) {
	c, err := ParseConfig(raw)
	if err != nil {
		return c, err
	}
	c.configFile, err = filepath.Abs(path)
	if err != nil {
		return Config{}, errors.New("invalid config path")
	}
	return c, nil
}

func ParseConfig(raw []byte) (Config, error) {
	var config Config
	if err := decodeConfigObject(raw, &config); err != nil {
		return Config{}, err
	}
	return config, config.Validate()
}

func decodeConfigObject(raw []byte, out any) error {
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) || decodeConfigJSON(raw, out) != nil {
		return errConfigJSON
	}
	return nil
}

func decodeConfigJSON(raw []byte, out any) error {
	if len(raw) > MaxConfigBytes || !utf8.Valid(raw) {
		return errConfigJSON
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if !scanConfigJSON(d, reflect.TypeOf(out).Elem(), 0) {
		return errConfigJSON
	}
	if _, err := d.Token(); err != io.EOF || json.Unmarshal(raw, out) != nil {
		return errConfigJSON
	}
	return nil
}

// encoding/json accepts duplicate keys and matches struct fields ignoring case.
// A token pass keeps those rules strict without folding case in dynamic maps.
func scanConfigJSON(d *json.Decoder, shape reflect.Type, depth int) bool {
	token, err := d.Token()
	if err != nil || depth > 64 {
		return false
	}
	if shape != nil && shape.Kind() == reflect.Pointer {
		shape = shape.Elem()
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			name, err := d.Token()
			key, ok := name.(string)
			if err != nil || !ok || seen[key] {
				return false
			}
			seen[key] = true
			field := configJSONField(shape, key)
			if (shape != nil && shape.Kind() == reflect.Struct && field == nil) || !scanConfigJSON(d, field, depth+1) {
				return false
			}
		}
	case json.Delim('['):
		var element reflect.Type
		if shape != nil && (shape.Kind() == reflect.Slice || shape.Kind() == reflect.Array) {
			element = shape.Elem()
		}
		for d.More() {
			if !scanConfigJSON(d, element, depth+1) {
				return false
			}
		}
	default:
		return true
	}
	_, err = d.Token()
	return err == nil
}

func configJSONField(shape reflect.Type, key string) reflect.Type {
	if shape == nil {
		return nil
	}
	switch shape.Kind() {
	case reflect.Map:
		return shape.Elem()
	case reflect.Struct:
		for i := range shape.NumField() {
			field := shape.Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if field.IsExported() && name != "" && name != "-" && name == key {
				return field.Type
			}
		}
	}
	return nil
}
