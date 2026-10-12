package product

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
)

// ResourceOverrides preserves integers beyond JavaScript's safe range. Every
// supplied value is a decimal string or null; null clears an override.
type ResourceOverrides map[ResourceDimension]*int64

func (values ResourceOverrides) MarshalJSON() ([]byte, error) {
	out := map[ResourceDimension]*string{}
	for dimension, value := range values {
		if value == nil {
			out[dimension] = nil
		} else {
			encoded := strconv.FormatInt(*value, 10)
			out[dimension] = &encoded
		}
	}
	return json.Marshal(out)
}
func (values *ResourceOverrides) UnmarshalJSON(raw []byte) error {
	var input map[ResourceDimension]*string
	if err := json.Unmarshal(raw, &input); err != nil {
		return err
	}
	out := ResourceOverrides{}
	for dimension, value := range input {
		if value == nil {
			out[dimension] = nil
			continue
		}
		number, err := strconv.ParseInt(*value, 10, 64)
		if err != nil || strconv.FormatInt(number, 10) != *value {
			return ErrInvalid
		}
		out[dimension] = &number
	}
	*values = out
	return nil
}

// A provided entitlement is a complete pair. Missing/null members cannot turn
// an accidentally incomplete edit into a zero allocation.
func (value *ResourceEntitlement) UnmarshalJSON(raw []byte) error {
	var input struct {
		Default *string `json:"default"`
		Ceiling *string `json:"ceiling"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrInvalid
	}
	if input.Default == nil || input.Ceiling == nil {
		return ErrInvalid
	}
	parse := func(raw string) (int64, error) {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || strconv.FormatInt(value, 10) != raw {
			return 0, ErrInvalid
		}
		return value, nil
	}
	initial, err := parse(*input.Default)
	if err != nil {
		return err
	}
	ceiling, err := parse(*input.Ceiling)
	if err != nil {
		return err
	}
	*value = ResourceEntitlement{Default: initial, Ceiling: ceiling}
	return nil
}
