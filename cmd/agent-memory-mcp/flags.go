package main

import (
	"fmt"
	"strconv"
)

type optionalFloat64 struct {
	value float64
	set   bool
}

func (o *optionalFloat64) String() string {
	if o == nil || !o.set {
		return ""
	}
	return strconv.FormatFloat(o.value, 'f', -1, 64)
}

func (o *optionalFloat64) Set(value string) error {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("invalid float value %q: %w", value, err)
	}
	o.value = parsed
	o.set = true
	return nil
}
