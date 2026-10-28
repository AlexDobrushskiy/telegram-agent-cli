package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// An order-preserving JSON tree, so rewriting settings.json keeps every other
// key where it was. Values: *object, []any, string, json.Number, bool, nil.

type member struct {
	key string
	val any
}

type object struct{ members []member }

func (o *object) get(k string) (any, bool) {
	for _, m := range o.members {
		if m.key == k {
			return m.val, true
		}
	}
	return nil, false
}

func (o *object) set(k string, v any) {
	for i := range o.members {
		if o.members[i].key == k {
			o.members[i].val = v
			return
		}
	}
	o.members = append(o.members, member{k, v})
}

func (o *object) del(k string) {
	for i, m := range o.members {
		if m.key == k {
			o.members = append(o.members[:i], o.members[i+1:]...)
			return
		}
	}
}

func (o *object) str(k string) string {
	v, _ := o.get(k)
	s, _ := v.(string)
	return s
}

func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := &object{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key %v is not a string", kt)
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				o.set(k, v)
			}
			_, err := dec.Token()
			return o, err
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			_, err := dec.Token()
			return arr, err
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return tok, nil
	}
}

func encodeJSON(v any, indent string) []byte {
	var b bytes.Buffer
	encodeValue(&b, v, indent, 0)
	return b.Bytes()
}

func encodeString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b.Truncate(b.Len() - 1) // Encode appends a newline
}

func encodeValue(b *bytes.Buffer, v any, indent string, depth int) {
	pad := strings.Repeat(indent, depth+1)
	switch t := v.(type) {
	case *object:
		if len(t.members) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, m := range t.members {
			b.WriteString(pad)
			encodeString(b, m.key)
			b.WriteString(": ")
			encodeValue(b, m.val, indent, depth+1)
			if i < len(t.members)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat(indent, depth) + "}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, e := range t {
			b.WriteString(pad)
			encodeValue(b, e, indent, depth+1)
			if i < len(t)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat(indent, depth) + "]")
	case string:
		encodeString(b, t)
	case json.Number:
		b.WriteString(t.String())
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case nil:
		b.WriteString("null")
	default:
		encodeString(b, fmt.Sprint(t))
	}
}

// detectIndent returns the indent unit of a pretty-printed JSON text (default two spaces).
func detectIndent(text string) string {
	for _, line := range strings.Split(text, "\n")[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && len(trimmed) < len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}
	return "  "
}
