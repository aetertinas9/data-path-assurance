package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// jsonKind is the kind of one decoded JSON value. null has no kind: the strict
// decoder rejects it wherever it appears.
type jsonKind int

const (
	jsonObject jsonKind = iota + 1
	jsonArray
	jsonString
	jsonNumber
	jsonBool
)

// jsonValue is one decoded JSON value. text holds a string's decoded value or
// a number's literal as written.
type jsonValue struct {
	kind    jsonKind
	text    string
	boolean bool
	items   []jsonValue
	members []jsonMember
}

// jsonMember is one object member, in document order.
type jsonMember struct {
	key   string
	value jsonValue
}

// maxJSONDepth bounds nesting. The manifest schema nests three levels, so a
// deeper document is invalid whatever it contains; the bound only keeps the
// recursive decoder shallow.
const maxJSONDepth = 64

// errStrictJSON reports a document outside the strict JSON discipline. Its
// messages never quote the document, so no fixture content reaches an error.
var errStrictJSON = errors.New("not strict JSON")

// decodeStrictJSON decodes one JSON document under the manifest discipline:
// valid UTF-8 with no byte order mark, no duplicate key in any object, no
// null anywhere, and nothing but whitespace after the value.
func decodeStrictJSON(data []byte) (jsonValue, error) {
	if !utf8.Valid(data) {
		return jsonValue{}, fmt.Errorf("%w: invalid UTF-8", errStrictJSON)
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return jsonValue{}, fmt.Errorf("%w: byte order mark", errStrictJSON)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec, 1)
	if err != nil {
		return jsonValue{}, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return jsonValue{}, fmt.Errorf("%w: data after the top-level value", errStrictJSON)
	}
	return v, nil
}

// decodeValue reads the next complete value from dec at nesting depth.
func decodeValue(dec *json.Decoder, depth int) (jsonValue, error) {
	if depth > maxJSONDepth {
		return jsonValue{}, fmt.Errorf("%w: nesting too deep", errStrictJSON)
	}
	tok, err := dec.Token()
	if err != nil {
		// The decoder's own message may quote document bytes; it is dropped.
		return jsonValue{}, fmt.Errorf("%w: syntax error", errStrictJSON)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return decodeObject(dec, depth)
		case '[':
			return decodeArray(dec, depth)
		default:
			return jsonValue{}, fmt.Errorf("%w: unexpected delimiter", errStrictJSON)
		}
	case string:
		return jsonValue{kind: jsonString, text: t}, nil
	case json.Number:
		return jsonValue{kind: jsonNumber, text: string(t)}, nil
	case bool:
		return jsonValue{kind: jsonBool, boolean: t}, nil
	case nil:
		return jsonValue{}, fmt.Errorf("%w: null value", errStrictJSON)
	default:
		return jsonValue{}, fmt.Errorf("%w: unexpected token", errStrictJSON)
	}
}

// decodeObject reads the members of an object whose '{' has been consumed.
func decodeObject(dec *json.Decoder, depth int) (jsonValue, error) {
	v := jsonValue{kind: jsonObject}
	seen := map[string]struct{}{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return jsonValue{}, fmt.Errorf("%w: syntax error", errStrictJSON)
		}
		key, ok := tok.(string)
		if !ok {
			return jsonValue{}, fmt.Errorf("%w: object key is not a string", errStrictJSON)
		}
		if _, dup := seen[key]; dup {
			return jsonValue{}, fmt.Errorf("%w: duplicate object key", errStrictJSON)
		}
		seen[key] = struct{}{}
		member, err := decodeValue(dec, depth+1)
		if err != nil {
			return jsonValue{}, err
		}
		v.members = append(v.members, jsonMember{key: key, value: member})
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return jsonValue{}, fmt.Errorf("%w: unterminated object", errStrictJSON)
	}
	return v, nil
}

// decodeArray reads the items of an array whose '[' has been consumed.
func decodeArray(dec *json.Decoder, depth int) (jsonValue, error) {
	v := jsonValue{kind: jsonArray}
	for dec.More() {
		item, err := decodeValue(dec, depth+1)
		if err != nil {
			return jsonValue{}, err
		}
		v.items = append(v.items, item)
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim(']') {
		return jsonValue{}, fmt.Errorf("%w: unterminated array", errStrictJSON)
	}
	return v, nil
}
