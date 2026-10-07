// Package pyjson writes JSON exactly as Python's json.dumps does with its default separators
// (", " and ": "), where the bytes matter: a digest of it is stored, or the archive's settings are
// compared with what the Python wrote. Values: nil, bool, ints, floats, string, []any (and other
// slices), map[string]any (keys in sorted order only when SortKeys; else as an OrderedMap gives).
package pyjson

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Pair is one key of an ordered object.
type Pair struct {
	Key   string
	Value any
}

// OrderedMap is an object whose keys keep their order, as a Python dict does.
type OrderedMap []Pair

// Dumps is json.dumps(v, ensure_ascii=asciiOnly).
func Dumps(v any, asciiOnly bool) string {
	var b strings.Builder
	write(&b, v, asciiOnly)
	return b.String()
}

func write(b *strings.Builder, v any, ascii bool) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeString(b, x, ascii)
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		b.WriteString(Float(x))
	case OrderedMap:
		b.WriteString("{")
		for i, p := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeString(b, p.Key, ascii)
			b.WriteString(": ")
			write(b, p.Value, ascii)
		}
		b.WriteString("}")
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		om := make(OrderedMap, 0, len(keys))
		for _, k := range keys {
			om = append(om, Pair{k, x[k]})
		}
		write(b, om, ascii)
	default:
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Slice, reflect.Array:
			b.WriteString("[")
			for i := 0; i < rv.Len(); i++ {
				if i > 0 {
					b.WriteString(", ")
				}
				write(b, rv.Index(i).Interface(), ascii)
			}
			b.WriteString("]")
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			b.WriteString(strconv.FormatInt(rv.Int(), 10))
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			b.WriteString(strconv.FormatUint(rv.Uint(), 10))
		case reflect.Float32:
			b.WriteString(Float(rv.Float()))
		default:
			b.WriteString(fmt.Sprintf("%q", fmt.Sprint(v)))
		}
	}
}

// Float is Python's repr of a float (as json.dumps writes it).
func Float(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	s := strconv.FormatFloat(f, 'e', -1, 64) // shortest digits
	mant, expS, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(expS)
	if exp < -4 || exp >= 16 {
		sign := "+"
		if exp < 0 {
			sign, exp = "-", -exp
		}
		return fmt.Sprintf("%se%s%02d", mant, sign, exp)
	}
	out := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(out, ".") {
		out += ".0"
	}
	return out
}

func writeString(b *strings.Builder, s string, ascii bool) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(b, `\u%04x`, r)
			case ascii && r > 0x7e:
				if r > 0xffff {
					a, c := utf16.EncodeRune(r)
					fmt.Fprintf(b, `\u%04x\u%04x`, a, c)
				} else {
					fmt.Fprintf(b, `\u%04x`, r)
				}
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}
