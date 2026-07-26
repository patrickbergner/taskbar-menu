package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// formatConfig rewrites path with 4-space pretty printing, collapsing any
// object or array that holds no nested object onto one line -- so a plain
// menu entry like { "label": "Firefox", "exec": "firefox.exe" } stays a
// single line instead of ballooning into one line per field, while an entry
// with an "items" submenu still expands so its children are readable.
//
// This is a textual reformat, not a round trip through Config: it preserves
// key order and unknown keys exactly as written, which a re-marshal of the
// decoded struct would not.
func formatConfig(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	body := decodeBOM(raw)

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	root, err := decodeOrdered(dec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		return 1
	}

	var buf bytes.Buffer
	writeValue(&buf, root, 0, false) // the root object always expands
	buf.WriteByte('\n')

	if bytes.Equal(buf.Bytes(), raw) {
		fmt.Printf("%s already formatted\n", path)
		return 0
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("formatted %s\n", path)
	return 0
}

// orderedObject is a JSON object that remembers the order its keys were
// written in, which a plain map[string]any would discard.
type orderedObject struct {
	keys []string
	vals map[string]any
}

func (o *orderedObject) set(key string, val any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, exists := o.vals[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = val
}

// decodeOrdered reads one JSON value from dec. Values are one of:
// *orderedObject, []any, string, json.Number, bool, or nil.
func decodeOrdered(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil // string, json.Number, bool, or nil
	}

	switch delim {
	case '{':
		obj := &orderedObject{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			val, err := decodeOrdered(dec)
			if err != nil {
				return nil, err
			}
			obj.set(keyTok.(string), val)
		}
		if _, err := dec.Token(); err != nil { // consume '}'
			return nil, err
		}
		return obj, nil
	case '[':
		arr := []any{}
		for dec.More() {
			val, err := decodeOrdered(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, val)
		}
		if _, err := dec.Token(); err != nil { // consume ']'
			return nil, err
		}
		return arr, nil
	}
	return nil, fmt.Errorf("unexpected delimiter %v", delim)
}

// hasNestedObject reports whether v is, or contains anywhere inside it, an
// object. It is what decides whether a container has to expand: an array of
// plain strings stays inline no matter how long, but a single nested object
// forces every ancestor array and object open so the object is readable.
func hasNestedObject(v any) bool {
	switch t := v.(type) {
	case *orderedObject:
		return true
	case []any:
		for _, e := range t {
			if hasNestedObject(e) {
				return true
			}
		}
	}
	return false
}

const formatIndent = "    "

// writeValue renders v at the given indent depth. forceBlock overrides the
// usual "no nested object => one line" rule, which only the document root
// needs: a config with no items or launchers yet is still worth keeping as a
// multi-line skeleton rather than collapsing to one line.
func writeValue(buf *bytes.Buffer, v any, depth int, forceBlock bool) {
	switch t := v.(type) {
	case *orderedObject:
		writeObject(buf, t, depth, forceBlock)
	case []any:
		writeArray(buf, t, depth, forceBlock)
	case string:
		writeJSONString(buf, t)
	case json.Number:
		buf.WriteString(t.String())
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	default:
		buf.WriteString("null")
	}
}

func writeObject(buf *bytes.Buffer, obj *orderedObject, depth int, forceBlock bool) {
	if len(obj.keys) == 0 {
		buf.WriteString("{}")
		return
	}

	compact := !forceBlock
	if compact {
		for _, k := range obj.keys {
			if hasNestedObject(obj.vals[k]) {
				compact = false
				break
			}
		}
	}

	if compact {
		buf.WriteString("{ ")
		for i, k := range obj.keys {
			if i > 0 {
				buf.WriteString(", ")
			}
			writeJSONString(buf, k)
			buf.WriteString(": ")
			writeValue(buf, obj.vals[k], depth, false)
		}
		buf.WriteString(" }")
		return
	}

	buf.WriteString("{\n")
	inner := depth + 1
	for i, k := range obj.keys {
		buf.WriteString(strings.Repeat(formatIndent, inner))
		writeJSONString(buf, k)
		buf.WriteString(": ")
		writeValue(buf, obj.vals[k], inner, false)
		if i < len(obj.keys)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString(strings.Repeat(formatIndent, depth))
	buf.WriteByte('}')
}

func writeArray(buf *bytes.Buffer, arr []any, depth int, forceBlock bool) {
	if len(arr) == 0 {
		buf.WriteString("[]")
		return
	}

	compact := !forceBlock
	if compact {
		for _, e := range arr {
			if hasNestedObject(e) {
				compact = false
				break
			}
		}
	}

	if compact {
		buf.WriteByte('[')
		for i, e := range arr {
			if i > 0 {
				buf.WriteString(", ")
			}
			writeValue(buf, e, depth, false)
		}
		buf.WriteByte(']')
		return
	}

	buf.WriteString("[\n")
	inner := depth + 1
	for i, e := range arr {
		buf.WriteString(strings.Repeat(formatIndent, inner))
		writeValue(buf, e, inner, false)
		if i < len(arr)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString(strings.Repeat(formatIndent, depth))
	buf.WriteByte(']')
}

// writeJSONString appends s as a JSON string literal without HTML escaping --
// this is a config file, not a browser payload, so a literal "&" or ">" in a
// URL should not turn into &.
func writeJSONString(buf *bytes.Buffer, s string) {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		// s came from a successfully decoded JSON document, so encoding it back
		// cannot fail.
		panic(err)
	}
	buf.Truncate(buf.Len() - 1) // Encode appends a trailing newline
}
