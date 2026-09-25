package offline

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// errUnknownKey is returned by an object field callback for a key the schema
// does not know; the reader reports it at the parent path.
var errUnknownKey = errors.New("unknown key")

// pathElem is one step of a field path: an object key or an array index.
type pathElem struct {
	key   string
	index int
	isIdx bool
}

// jsonReader walks one JSON document under a schema supplied by the caller.
// It rejects a byte order mark, invalid UTF-8, duplicate keys, null values,
// type mismatches and anything but JSON whitespace after the top-level value.
// Errors carry the schema field path only, never input content.
type jsonReader struct {
	dec   *json.Decoder
	class error
	path  []pathElem
}

func newJSONReader(data []byte, class error) (*jsonReader, error) {
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		return nil, inputError(class, "input starts with a byte order mark")
	}
	if !utf8.Valid(data) {
		return nil, inputError(class, "input is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return &jsonReader{dec: dec, class: class}, nil
}

// pathString renders the current path, e.g. "frames[0].payload.assets[3]".
func (r *jsonReader) pathString() string {
	var b strings.Builder
	for _, e := range r.path {
		if e.isIdx {
			b.WriteByte('[')
			b.WriteString(strconv.Itoa(e.index))
			b.WriteByte(']')
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(e.key)
	}
	if b.Len() == 0 {
		return "top level"
	}
	return b.String()
}

// fail builds an input error at the current path.
func (r *jsonReader) fail(problem string) error {
	return inputError(r.class, r.pathString()+": "+problem)
}

func (r *jsonReader) token() (json.Token, error) {
	t, err := r.dec.Token()
	if err != nil {
		return nil, r.fail("malformed JSON")
	}
	return t, nil
}

// object reads an object, calling field once per key with the reader
// positioned at that key's value. field must consume the value or return an
// error; it returns errUnknownKey for keys outside the schema. Duplicate keys
// are rejected after escape decoding, comparing bytes.
func (r *jsonReader) object(field func(key string) error) error {
	return r.readObject(true, field)
}

// mapObject reads an object whose keys are data rather than schema names.
// Errors inside it are reported at the object's own path, so that no key read
// from the input appears in a message.
func (r *jsonReader) mapObject(field func(key string) error) error {
	return r.readObject(false, field)
}

func (r *jsonReader) readObject(schemaKeys bool, field func(key string) error) error {
	t, err := r.token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return r.fail(typeProblem(t, "an object"))
	}
	seen := make(map[string]struct{})
	for r.dec.More() {
		kt, err := r.token()
		if err != nil {
			return err
		}
		key, ok := kt.(string)
		if !ok {
			return r.fail("malformed JSON")
		}
		if _, dup := seen[key]; dup {
			return r.fail("duplicate key")
		}
		seen[key] = struct{}{}
		if schemaKeys {
			r.path = append(r.path, pathElem{key: key})
		}
		ferr := field(key)
		if schemaKeys {
			r.path = r.path[:len(r.path)-1]
		}
		if ferr != nil {
			if errors.Is(ferr, errUnknownKey) {
				return r.fail("unknown key")
			}
			return ferr
		}
	}
	t, err = r.token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != '}' {
		return r.fail("malformed JSON")
	}
	return nil
}

// array reads an array, calling elem once per element with the reader
// positioned at it. At most max elements are accepted.
func (r *jsonReader) array(max int, elem func(i int) error) error {
	t, err := r.token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != '[' {
		return r.fail(typeProblem(t, "an array"))
	}
	i := 0
	for r.dec.More() {
		if i >= max {
			return r.fail("too many elements")
		}
		r.path = append(r.path, pathElem{index: i, isIdx: true})
		err := elem(i)
		r.path = r.path[:len(r.path)-1]
		if err != nil {
			return err
		}
		i++
	}
	t, err = r.token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != ']' {
		return r.fail("malformed JSON")
	}
	return nil
}

// str reads a string value (escapes decoded).
func (r *jsonReader) str() (string, error) {
	t, err := r.token()
	if err != nil {
		return "", err
	}
	s, ok := t.(string)
	if !ok {
		return "", r.fail(typeProblem(t, "a string"))
	}
	return s, nil
}

// boolean reads a boolean value.
func (r *jsonReader) boolean() (bool, error) {
	t, err := r.token()
	if err != nil {
		return false, err
	}
	b, ok := t.(bool)
	if !ok {
		return false, r.fail(typeProblem(t, "a boolean"))
	}
	return b, nil
}

// uintNumber reads a JSON number whose literal is 0|[1-9][0-9]* and whose
// value lies in [min, max].
func (r *jsonReader) uintNumber(min, max uint64) (uint64, error) {
	t, err := r.token()
	if err != nil {
		return 0, err
	}
	n, ok := t.(json.Number)
	if !ok {
		return 0, r.fail(typeProblem(t, "an integer number"))
	}
	v, ok := parseDecimalUint(string(n))
	if !ok || v < min || v > max {
		return 0, r.fail("integer is malformed or out of range")
	}
	return v, nil
}

// end requires that nothing but JSON whitespace follows the top-level value.
func (r *jsonReader) end() error {
	r.path = r.path[:0]
	if _, err := r.dec.Token(); err != io.EOF {
		return r.fail("data after the top-level value")
	}
	return nil
}

// typeProblem describes a token that is not of the wanted type without
// echoing the token itself.
func typeProblem(t json.Token, want string) string {
	if t == nil {
		return "null is not allowed"
	}
	return "value is not " + want
}

// parseDecimalUint parses 0|[1-9][0-9]* exactly into a uint64.
func parseDecimalUint(s string) (uint64, bool) {
	if s == "" || len(s) > 20 {
		return 0, false
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseDecimalInt parses 0|-?[1-9][0-9]* exactly into an int64.
func parseDecimalInt(s string) (int64, bool) {
	digits := s
	if strings.HasPrefix(s, "-") {
		digits = s[1:]
		if digits == "0" {
			return 0, false
		}
	}
	if _, ok := parseDecimalUint(digits); !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
