// Package frames builds rig/1 frames from slices of structs, a column per json-tagged field.
package frames

import (
	"reflect"
	"strings"
	"time"

	"github.com/goxang/rig/sdk"
)

var (
	timeType = reflect.TypeOf(time.Time{})
	durType  = reflect.TypeOf(time.Duration(0))
)

// Of is a frame with a row per item; a field tagged `unit:"ms"` carries that unit.
func Of[T any](items []T) *sdk.Frame {
	t := reflect.TypeOf((*T)(nil)).Elem()
	var fields []sdk.Field
	var index []int
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		fields = append(fields, sdk.Col(name, typeOf(f.Type), f.Tag.Get("unit")))
		index = append(index, i)
	}
	fr := sdk.NewFrame(fields...)
	for _, it := range items {
		v := reflect.ValueOf(it)
		row := make([]any, len(index))
		for j, i := range index {
			fv := v.Field(i)
			if fv.Type() == timeType && fv.Interface().(time.Time).IsZero() {
				continue
			}
			row[j] = fv.Interface()
		}
		fr.Append(row...)
	}
	return fr
}

func typeOf(t reflect.Type) string {
	switch {
	case t == timeType:
		return sdk.TypeTime
	case t == durType:
		return sdk.TypeDuration
	}
	switch t.Kind() {
	case reflect.String:
		return sdk.TypeString
	case reflect.Bool:
		return sdk.TypeBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return sdk.TypeInt
	case reflect.Float32, reflect.Float64:
		return sdk.TypeNumber
	}
	return sdk.TypeJSON
}

type line struct {
	Line string `json:"line"`
}

// Text is out as a frame of one line column, a row per line (a diff, a log).
func Text(out string) *sdk.Frame {
	var ls []line
	for _, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		ls = append(ls, line{l})
	}
	return Of(ls)
}
