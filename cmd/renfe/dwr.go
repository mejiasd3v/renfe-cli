package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// field keeps object keys in the order the website's JavaScript sends them.
type field struct {
	Key   string
	Value any
}

// dwrBody encodes one DWR 3 "plaincall" in the same order as /vol/dwr/engine.js.
func dwrBody(script, method string, args []any, batchID int, page, scriptSession string) string {
	var lines []string
	n := 0
	var encode func(any) string
	encode = func(v any) string {
		switch v := v.(type) {
		case nil:
			return "null:null"
		case bool:
			return "boolean:" + strconv.FormatBool(v)
		case int:
			return "number:" + strconv.Itoa(v)
		case string:
			return "string:" + encodeURIComponent(v)
		case []any:
			refs := make([]string, len(v))
			for i, item := range v {
				n++
				name := fmt.Sprintf("c0-e%d", n)
				lines = append(lines, name+"="+encode(item))
				refs[i] = "reference:" + name
			}
			return "array:[" + strings.Join(refs, ",") + "]"
		case []field:
			refs := make([]string, len(v))
			for i, f := range v {
				n++
				name := fmt.Sprintf("c0-e%d", n)
				lines = append(lines, name+"="+encode(f.Value))
				refs[i] = encodeURIComponent(f.Key) + ":reference:" + name
			}
			return "Object_Object:{" + strings.Join(refs, ", ") + "}"
		}
		panic(fmt.Sprintf("dwr: unsupported type %T", v))
	}
	head := []string{"callCount=1", "nextReverseAjaxIndex=0", "c0-scriptName=" + script, "c0-methodName=" + method, "c0-id=0"}
	for i, arg := range args {
		param := encode(arg)
		lines = append(lines, fmt.Sprintf("c0-param%d=%s", i, param))
	}
	tail := []string{"batchId=" + strconv.Itoa(batchID), "instanceId=0", "page=" + encodeURIComponent(page), "scriptSessionId=" + scriptSession, "windowName="}
	return strings.Join(append(append(head, lines...), tail...), "\n") + "\n"
}

// encodeURIComponent matches JavaScript's function of the same name.
func encodeURIComponent(s string) string {
	const unreserved = "-_.!~*'()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// dwrReply extracts the value passed to r.handleCallback in a DWR response, as JSON.
// A server-side exception becomes an error carrying the website's message.
func dwrReply(body []byte) (json.RawMessage, error) {
	s := string(body)
	start := strings.Index(s, "//#DWR-REPLY")
	if start < 0 {
		return nil, errors.New("unexpected DWR response (no reply marker)")
	}
	s = s[start:]
	call := strings.Index(s, "r.handle")
	if call < 0 {
		return nil, errors.New("unexpected DWR response (no handler call)")
	}
	s = s[call+len("r."):]
	open := strings.IndexByte(s, '(')
	if open < 0 {
		return nil, errors.New("unexpected DWR response (malformed handler call)")
	}
	handler := s[:open]
	p := &jsParser{s: s[open+1:]}
	var args []json.RawMessage
	for {
		var out bytes.Buffer
		if err := p.value(&out); err != nil {
			return nil, fmt.Errorf("unexpected DWR response: %w", err)
		}
		args = append(args, out.Bytes())
		p.space()
		if p.eat(')') {
			break
		}
		if !p.eat(',') {
			return nil, errors.New("unexpected DWR response (bad argument list)")
		}
	}
	switch {
	case handler == "handleCallback" && len(args) == 3:
		return args[2], nil
	case handler == "handleException" && len(args) == 3:
		return nil, dwrException(args[2])
	case handler == "handleBatchException" && len(args) >= 1:
		return nil, dwrException(args[0])
	}
	return nil, fmt.Errorf("unexpected DWR handler %s", handler)
}

func dwrException(raw json.RawMessage) error {
	var ex struct {
		Message   string `json:"message"`
		MsgError  string `json:"msgError"`
		CdgoError string `json:"cdgoError"`
		Name      string `json:"name"`
	}
	_ = json.Unmarshal(raw, &ex) // A non-object exception still yields a generic error below.
	msg := strings.TrimSpace(firstNonEmpty(ex.MsgError, ex.Message, ex.Name))
	if msg == "" {
		msg = "unknown error"
	}
	if ex.CdgoError != "" {
		return fmt.Errorf("Renfe error %s: %s", ex.CdgoError, msg)
	}
	return fmt.Errorf("Renfe error: %s", msg)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// jsParser converts the JavaScript object literals DWR emits (unquoted keys,
// JS string escapes, new Date(ms)) into JSON.
type jsParser struct {
	s string
	i int
}

func (p *jsParser) space() {
	for p.i < len(p.s) && strings.IndexByte(" \t\r\n", p.s[p.i]) >= 0 {
		p.i++
	}
}

func (p *jsParser) eat(c byte) bool {
	p.space()
	if p.i < len(p.s) && p.s[p.i] == c {
		p.i++
		return true
	}
	return false
}

func (p *jsParser) value(out *bytes.Buffer) error {
	p.space()
	if p.i >= len(p.s) {
		return errors.New("unexpected end of input")
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.object(out)
	case c == '[':
		return p.array(out)
	case c == '"' || c == '\'':
		str, err := p.str()
		if err != nil {
			return err
		}
		data, _ := json.Marshal(str)
		out.Write(data)
		return nil
	case c == '-' || c >= '0' && c <= '9':
		start := p.i
		p.i++
		for p.i < len(p.s) && strings.IndexByte("0123456789.eE+-", p.s[p.i]) >= 0 {
			p.i++
		}
		num := p.s[start:p.i]
		if !json.Valid([]byte(num)) {
			return fmt.Errorf("invalid number %q", num)
		}
		out.WriteString(num)
		return nil
	}
	word := p.ident()
	switch word {
	case "true", "false", "null":
		out.WriteString(word)
		return nil
	case "undefined":
		out.WriteString("null")
		return nil
	case "new":
		if p.ident() != "Date" || !p.eat('(') {
			return errors.New("unsupported constructor")
		}
		if err := p.value(out); err != nil {
			return err
		}
		if !p.eat(')') {
			return errors.New("unterminated Date")
		}
		return nil
	}
	return fmt.Errorf("unexpected token at offset %d", p.i)
}

func (p *jsParser) ident() string {
	p.space()
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if !(c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || p.i > start && c >= '0' && c <= '9') {
			break
		}
		p.i++
	}
	return p.s[start:p.i]
}

func (p *jsParser) object(out *bytes.Buffer) error {
	p.i++ // {
	out.WriteByte('{')
	if p.eat('}') {
		out.WriteByte('}')
		return nil
	}
	for {
		p.space()
		var key string
		if p.i < len(p.s) && (p.s[p.i] == '"' || p.s[p.i] == '\'') {
			k, err := p.str()
			if err != nil {
				return err
			}
			key = k
		} else if key = p.ident(); key == "" {
			return fmt.Errorf("expected object key at offset %d", p.i)
		}
		if !p.eat(':') {
			return fmt.Errorf("expected ':' after key %q", key)
		}
		data, _ := json.Marshal(key)
		out.Write(data)
		out.WriteByte(':')
		if err := p.value(out); err != nil {
			return err
		}
		if p.eat('}') {
			out.WriteByte('}')
			return nil
		}
		if !p.eat(',') {
			return fmt.Errorf("expected ',' or '}' at offset %d", p.i)
		}
		out.WriteByte(',')
	}
}

func (p *jsParser) array(out *bytes.Buffer) error {
	p.i++ // [
	out.WriteByte('[')
	if p.eat(']') {
		out.WriteByte(']')
		return nil
	}
	for {
		if err := p.value(out); err != nil {
			return err
		}
		if p.eat(']') {
			out.WriteByte(']')
			return nil
		}
		if !p.eat(',') {
			return fmt.Errorf("expected ',' or ']' at offset %d", p.i)
		}
		out.WriteByte(',')
	}
}

func (p *jsParser) str() (string, error) {
	quote := p.s[p.i]
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		p.i++
		switch {
		case c == quote:
			return b.String(), nil
		case c != '\\':
			b.WriteByte(c)
			continue
		}
		if p.i >= len(p.s) {
			break
		}
		e := p.s[p.i]
		p.i++
		switch e {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'x', 'u':
			size := 2
			if e == 'u' {
				size = 4
			}
			if p.i+size > len(p.s) {
				return "", errors.New("truncated string escape")
			}
			code, err := strconv.ParseUint(p.s[p.i:p.i+size], 16, 32)
			if err != nil {
				return "", errors.New("invalid string escape")
			}
			p.i += size
			r := rune(code)
			if utf16.IsSurrogate(r) && strings.HasPrefix(p.s[p.i:], `\u`) && p.i+6 <= len(p.s) {
				if low, err := strconv.ParseUint(p.s[p.i+2:p.i+6], 16, 32); err == nil {
					if pair := utf16.DecodeRune(r, rune(low)); pair != utf8.RuneError {
						r = pair
						p.i += 6
					}
				}
			}
			b.WriteRune(r)
		default:
			b.WriteByte(e) // \" \' \\ \/
		}
	}
	return "", errors.New("unterminated string")
}
