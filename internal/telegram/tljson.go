// Ports everysaid/telegram_store.py (plain, dump): gotd's objects written as the JSON that Telethon's
// to_dict() gave, so that telegram.db reads the same whichever wrote it.
//
// Telethon's dict of an object is {"_": its class name, then its fields}: the class name is the TL
// name without its namespace, camel-cased ("messageMediaPhoto" -> "MessageMediaPhoto"); the fields
// carry their TL names ("self" and "from" become "is_self" and "from_"), the required ones first,
// then the optional ones (flags.N?) and random_id, each group in the schema's order. An optional field not set
// is null, an optional list not set is [] and a flags.N?true is always true or false. plain() then
// leaves out every binary value (file references, inline thumbnails): the key goes, or the item of
// a list. Dates are Unix seconds, as gotd has them. All of this is read from gotd's own type
// information (TypeInfo, the Set/Get methods), never written per type.
package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/gotd/td/tdp"
	"github.com/gotd/td/tg"
)

// field is one field of a gotd type as Telethon's dict has it.
type field struct {
	goName   string
	key      string // Telethon's name
	index    []int  // in the struct
	optional bool   // a flags.N? field
	trueFlag bool   // a flags.N?true: Telethon says true or false, never null
}

type typeMeta struct {
	name   string // Telethon's class name
	fields []field
}

var metas sync.Map // reflect.Type (pointer to the struct) -> *typeMeta

var camelRe = regexp.MustCompile(`_([a-z])`)

// telethonName is Telethon's class name of a TL name (its snake_to_camel_case, namespace dropped).
func telethonName(tl string) string {
	if i := strings.LastIndexByte(tl, '.'); i >= 0 {
		tl = tl[i+1:]
	}
	s := camelRe.ReplaceAllStringFunc(tl, func(m string) string { return strings.ToUpper(m[1:]) })
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ReplaceAll(s[1:], "_", "")
}

// telethonKey is the name Telethon gives a TL field.
func telethonKey(schema string) string {
	switch schema {
	case "self":
		return "is_self"
	case "from":
		return "from_"
	}
	return schema
}

// meta is how Telethon writes objects of a gotd type (a pointer to a struct).
func meta(t reflect.Type) *typeMeta {
	if m, ok := metas.Load(t); ok {
		return m.(*typeMeta)
	}
	zero := reflect.New(t.Elem()).Interface().(tdp.Object)
	info := zero.TypeInfo()        // on a zero value every optional field says it is not set
	var required, optional []field // optional: Telethon's arguments with a default
	for _, f := range info.Fields {
		sf, ok := t.Elem().FieldByName(f.Name)
		if !ok {
			continue
		}
		fd := field{goName: f.Name, key: telethonKey(f.SchemaName), index: sf.Index, optional: f.Null}
		if fd.optional && sf.Type.Kind() == reflect.Bool {
			// a flags.N?Bool has a getter saying whether it is set; a flags.N?true has not
			get, ok := t.MethodByName("Get" + f.Name)
			fd.trueFlag = !ok || get.Type.NumOut() == 1
		}
		if fd.optional || f.SchemaName == "random_id" { // Telethon fills random_id itself: an argument with a default
			optional = append(optional, fd)
		} else {
			required = append(required, fd)
		}
	}
	m := &typeMeta{name: telethonName(info.Name), fields: append(required, optional...)}
	metas.Store(t, m)
	return m
}

// Dump is an object as telegram.db keeps it: Telethon's to_dict() made JSON by plain(), written as
// Python's json.dumps(ensure_ascii=False, separators=(",", ":")) writes it.
func Dump(obj tdp.Object) string {
	var b bytes.Buffer
	writeValue(&b, reflect.ValueOf(obj))
	return b.String()
}

var objectType = reflect.TypeOf((*tdp.Object)(nil)).Elem()

func isBytes(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8
}

// writeValue writes a value; false when it is binary and so left out.
func writeValue(b *bytes.Buffer, v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			b.WriteString("null")
			return true
		}
		return writeValue(b, v.Elem())
	case reflect.Pointer:
		if v.IsNil() {
			b.WriteString("null")
			return true
		}
		if v.Type().Implements(objectType) {
			writeObject(b, v)
			return true
		}
		return writeValue(b, v.Elem())
	case reflect.Struct:
		p := reflect.New(v.Type())
		p.Elem().Set(v)
		if p.Type().Implements(objectType) {
			writeObject(b, p)
			return true
		}
		b.WriteString("null")
		return true
	case reflect.Slice:
		if isBytes(v.Type()) {
			return false
		}
		b.WriteByte('[')
		first := true
		for i := 0; i < v.Len(); i++ {
			var item bytes.Buffer
			if !writeValue(&item, v.Index(i)) {
				continue
			}
			if !first {
				b.WriteByte(',')
			}
			first = false
			b.Write(item.Bytes())
		}
		b.WriteByte(']')
		return true
	case reflect.Array: // int128, int256: Telethon reads them as signed little-endian integers
		raw := make([]byte, v.Len())
		for i := range raw {
			raw[i] = byte(v.Index(i).Uint())
		}
		b.WriteString(signedLE(raw).String())
		return true
	case reflect.Bool:
		if v.Bool() {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
		return true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.WriteString(strconv.FormatInt(v.Int(), 10))
		return true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		b.WriteString(strconv.FormatUint(v.Uint(), 10))
		return true
	case reflect.Float32, reflect.Float64:
		b.WriteString(pyFloat(v.Float()))
		return true
	case reflect.String:
		writeString(b, v.String())
		return true
	}
	b.WriteString("null")
	return true
}

func writeObject(b *bytes.Buffer, p reflect.Value) {
	m := meta(p.Type())
	info := p.Interface().(tdp.Object).TypeInfo()
	unset := map[string]bool{}
	for _, f := range info.Fields {
		if f.Null {
			unset[f.Name] = true
		}
	}
	b.WriteString(`{"_":`)
	writeString(b, m.name)
	s := p.Elem()
	for _, f := range m.fields {
		fv := s.FieldByIndex(f.index)
		var item bytes.Buffer
		switch {
		case f.trueFlag:
			writeValue(&item, fv)
		case f.optional && unset[f.goName]:
			if fv.Kind() == reflect.Slice && !isBytes(fv.Type()) {
				item.WriteString("[]")
			} else {
				item.WriteString("null")
			}
		default:
			if !writeValue(&item, fv) {
				continue // binary: plain() leaves the key out
			}
		}
		b.WriteByte(',')
		writeString(b, f.key)
		b.WriteByte(':')
		b.Write(item.Bytes())
	}
	b.WriteByte('}')
}

func signedLE(raw []byte) *big.Int {
	be := make([]byte, len(raw))
	for i, c := range raw {
		be[len(raw)-1-i] = c
	}
	n := new(big.Int).SetBytes(be)
	if len(raw) > 0 && raw[len(raw)-1]&0x80 != 0 {
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), uint(8*len(raw))))
	}
	return n
}

// writeString writes a JSON string as Python's json.dumps(ensure_ascii=False) does. Bytes that are
// not UTF-8 become U+FFFD, as Telethon decodes strings (errors='replace').
func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
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
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r) // RuneError (an invalid byte) is written as U+FFFD
			}
		}
	}
	b.WriteByte('"')
}

// pyFloat is Python's repr of a float, as json.dumps writes it.
func pyFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	sign := ""
	if s[0] == '-' {
		sign, s = "-", s[1:]
	}
	mant, e, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(e)
	digits := strings.Replace(mant, ".", "", 1)
	decpt := exp + 1 // the value is 0.<digits> * 10^decpt
	if decpt <= -4 || decpt > 16 {
		out := digits[:1]
		if len(digits) > 1 {
			out += "." + digits[1:]
		}
		es := "+"
		if exp < 0 {
			es, exp = "-", -exp
		}
		return fmt.Sprintf("%s%se%s%02d", sign, out, es, exp)
	}
	switch {
	case decpt <= 0:
		return sign + "0." + strings.Repeat("0", -decpt) + digits
	case decpt >= len(digits):
		return sign + digits + strings.Repeat("0", decpt-len(digits)) + ".0"
	}
	return sign + digits[:decpt] + "." + digits[decpt:]
}

// --- the other way: Telethon's JSON into gotd's objects ---------------------------------------------

var (
	byNameOnce sync.Once
	byName     map[string][]reflect.Type // Telethon's class name -> gotd types (a few names are shared)
)

func classes() map[string][]reflect.Type {
	byNameOnce.Do(func() {
		byName = map[string][]reflect.Type{}
		for _, make := range tg.TypesConstructorMap() {
			obj := make()
			o, ok := obj.(tdp.Object)
			if !ok {
				continue
			}
			t := reflect.TypeOf(obj)
			name := telethonName(o.TypeInfo().Name)
			byName[name] = append(byName[name], t)
		}
	})
	return byName
}

// Load is an object of telegram.db's JSON as gotd's: the reverse of Dump. Binary values (left out
// by plain()) come back empty but set, so that Dump leaves them out again.
func Load(data []byte) (tdp.Object, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	out, err := load(v, objectType)
	if err != nil {
		return nil, err
	}
	return out.Interface().(tdp.Object), nil
}

func load(v any, t reflect.Type) (reflect.Value, error) {
	switch t.Kind() {
	case reflect.Interface:
		if v == nil {
			return reflect.Zero(t), nil
		}
		m, ok := v.(map[string]any)
		if !ok {
			return reflect.Value{}, fmt.Errorf("an object expected for %s", t)
		}
		name, _ := m["_"].(string)
		for _, c := range classes()[name] {
			if c.Implements(t) {
				return loadObject(m, c)
			}
		}
		return reflect.Value{}, fmt.Errorf("unknown class %s for %s", name, t)
	case reflect.Pointer:
		if v == nil {
			return reflect.Zero(t), nil
		}
		m, ok := v.(map[string]any)
		if !ok {
			return reflect.Value{}, fmt.Errorf("an object expected for %s", t)
		}
		return loadObject(m, t)
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return reflect.Value{}, fmt.Errorf("an object expected for %s", t)
		}
		p, err := loadObject(m, reflect.PointerTo(t))
		if err != nil {
			return reflect.Value{}, err
		}
		return p.Elem(), nil
	case reflect.Slice:
		if isBytes(t) {
			return reflect.ValueOf([]byte{}).Convert(t), nil
		}
		list, ok := v.([]any)
		if !ok {
			return reflect.Value{}, fmt.Errorf("a list expected for %s", t)
		}
		out := reflect.MakeSlice(t, 0, len(list))
		for _, item := range list {
			x, err := load(item, t.Elem())
			if err != nil {
				return reflect.Value{}, err
			}
			out = reflect.Append(out, x)
		}
		return out, nil
	case reflect.Array:
		n, ok := v.(json.Number)
		bi, ok2 := new(big.Int).SetString(string(n), 10)
		if !ok || !ok2 {
			return reflect.Value{}, fmt.Errorf("an integer expected for %s", t)
		}
		size := t.Len()
		if bi.Sign() < 0 {
			bi.Add(bi, new(big.Int).Lsh(big.NewInt(1), uint(8*size)))
		}
		be := bi.FillBytes(make([]byte, size))
		out := reflect.New(t).Elem()
		for i := 0; i < size; i++ {
			out.Index(i).SetUint(uint64(be[size-1-i]))
		}
		return out, nil
	case reflect.Bool:
		b, ok := v.(bool)
		if !ok {
			return reflect.Value{}, fmt.Errorf("a bool expected for %s", t)
		}
		return reflect.ValueOf(b).Convert(t), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, ok := v.(json.Number)
		i, err := n.Int64()
		if !ok || err != nil {
			return reflect.Value{}, fmt.Errorf("an integer expected for %s", t)
		}
		out := reflect.New(t).Elem()
		out.SetInt(i)
		return out, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, ok := v.(json.Number)
		i, err := strconv.ParseUint(string(n), 10, 64)
		if !ok || err != nil {
			return reflect.Value{}, fmt.Errorf("an integer expected for %s", t)
		}
		out := reflect.New(t).Elem()
		out.SetUint(i)
		return out, nil
	case reflect.Float32, reflect.Float64:
		n, ok := v.(json.Number)
		f, err := n.Float64()
		if !ok || err != nil {
			return reflect.Value{}, fmt.Errorf("a number expected for %s", t)
		}
		out := reflect.New(t).Elem()
		out.SetFloat(f)
		return out, nil
	case reflect.String:
		s, ok := v.(string)
		if !ok {
			return reflect.Value{}, fmt.Errorf("a string expected for %s", t)
		}
		return reflect.ValueOf(s).Convert(t), nil
	}
	return reflect.Value{}, fmt.Errorf("cannot read %s", t)
}

func loadObject(m map[string]any, t reflect.Type) (reflect.Value, error) {
	tm := meta(t)
	if name, _ := m["_"].(string); name != tm.name {
		return reflect.Value{}, fmt.Errorf("%s where %s was expected", name, tm.name)
	}
	known := map[string]bool{"_": true}
	p := reflect.New(t.Elem())
	for _, f := range tm.fields {
		known[f.key] = true
		raw, present := m[f.key]
		fv := p.Elem().FieldByIndex(f.index)
		if !present {
			if !isBytes(fv.Type()) {
				return reflect.Value{}, fmt.Errorf("%s.%s missing", tm.name, f.key)
			}
			raw = nil // binary, left out by plain(): there was a value
		} else if raw == nil && f.optional {
			continue // not set
		} else if list, ok := raw.([]any); ok && len(list) == 0 && f.optional {
			continue // an optional list not set says [] too
		}
		if !f.optional {
			x, err := load(raw, fv.Type())
			if err != nil {
				return reflect.Value{}, fmt.Errorf("%s.%s: %w", tm.name, f.key, err)
			}
			fv.Set(x)
			continue
		}
		set := p.MethodByName("Set" + f.goName)
		if !set.IsValid() || set.Type().NumIn() != 1 {
			return reflect.Value{}, fmt.Errorf("%s.%s: no setter", tm.name, f.key)
		}
		x, err := load(raw, set.Type().In(0))
		if err != nil {
			return reflect.Value{}, fmt.Errorf("%s.%s: %w", tm.name, f.key, err)
		}
		set.Call([]reflect.Value{x})
	}
	for k := range m {
		if !known[k] {
			return reflect.Value{}, fmt.Errorf("%s.%s unknown", tm.name, k)
		}
	}
	return p, nil
}
