/*
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package cwmp is a SOAP 1.1 codec for the CWMP (TR-069) messages the ACS
// sends and receives.
package cwmp

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	NSSoapEnv = "http://schemas.xmlsoap.org/soap/envelope/"
	NSSoapEnc = "http://schemas.xmlsoap.org/soap/encoding/"
	NSXSD     = "http://www.w3.org/2001/XMLSchema"
	NSXSI     = "http://www.w3.org/2001/XMLSchema-instance"

	NSCwmp10 = "urn:dslforum-org:cwmp-1-0"
	NSCwmp11 = "urn:dslforum-org:cwmp-1-1"
	NSCwmp12 = "urn:dslforum-org:cwmp-1-2"
	NSCwmp13 = "urn:dslforum-org:cwmp-1-3"
	NSCwmp14 = "urn:dslforum-org:cwmp-1-4"

	cwmpNSPrefix = "urn:dslforum-org:cwmp-"
)

// ErrEmpty is returned by Decode for an empty HTTP body, which in CWMP means
// the CPE has no more requests.
var ErrEmpty = errors.New("empty CWMP message")

// Envelope is a decoded or to-be-encoded SOAP envelope.
type Envelope struct {
	// ID is the cwmp:ID header that pairs a request with its response.
	ID string
	// Namespace is the CWMP namespace URI of the message. Decode takes it from
	// the message; Encode defaults to cwmp-1-0 when it is empty.
	Namespace string
	// HoldRequests is the cwmp:HoldRequests header (ACS to CPE only).
	HoldRequests bool
	// NoMoreRequests is the cwmp:NoMoreRequests header of cwmp-1-0 CPEs.
	NoMoreRequests bool
	Body           Message
}

// IsEmpty reports whether an HTTP body is an empty CWMP message.
func IsEmpty(body []byte) bool {
	return len(bytes.TrimSpace(body)) == 0
}

// Decode parses a SOAP envelope. Element names are matched by local name so
// that any prefix and any cwmp-1-x namespace is accepted.
func Decode(data []byte) (*Envelope, error) {
	if IsEmpty(data) {
		return nil, ErrEmpty
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	d.CharsetReader = charsetReader
	// Some CPEs emit unescaped '&' or undeclared entities in string values.
	d.Strict = false

	env := &Envelope{}
	sawEnvelope, sawBody := false, false
	var section string
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode envelope: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case !sawEnvelope:
				if t.Name.Local != "Envelope" {
					return nil, fmt.Errorf("decode envelope: root element is %q, not Envelope", t.Name.Local)
				}
				sawEnvelope = true
			case section == "" && (t.Name.Local == "Header" || t.Name.Local == "Body"):
				section = t.Name.Local
				if section == "Body" {
					sawBody = true
				}
			case section == "Header":
				if err := decodeHeader(d, t, env); err != nil {
					return nil, err
				}
			case section == "Body":
				if env.Body != nil {
					if err := d.Skip(); err != nil {
						return nil, fmt.Errorf("decode body: %w", err)
					}
					continue
				}
				if err := decodeBody(d, t, env); err != nil {
					return nil, err
				}
			default:
				if err := d.Skip(); err != nil {
					return nil, fmt.Errorf("decode envelope: %w", err)
				}
			}
		case xml.EndElement:
			if t.Name.Local == section {
				section = ""
			}
		}
	}
	if !sawEnvelope {
		return nil, errors.New("decode envelope: no Envelope element")
	}
	if !sawBody || env.Body == nil {
		return nil, errors.New("decode envelope: no message in Body")
	}
	return env, nil
}

func decodeHeader(d *xml.Decoder, start xml.StartElement, env *Envelope) error {
	var text string
	if err := d.DecodeElement(&text, &start); err != nil {
		return fmt.Errorf("decode header %s: %w", start.Name.Local, err)
	}
	text = strings.TrimSpace(text)
	switch start.Name.Local {
	case "ID":
		env.ID = text
		setNamespace(env, start.Name.Space)
	case "HoldRequests":
		env.HoldRequests = parseBool(text)
	case "NoMoreRequests":
		env.NoMoreRequests = parseBool(text)
	}
	return nil
}

func decodeBody(d *xml.Decoder, start xml.StartElement, env *Envelope) error {
	method := start.Name.Local
	msg := newMessage(method)
	if msg == nil {
		if err := d.Skip(); err != nil {
			return fmt.Errorf("decode %s: %w", method, err)
		}
		msg = &Unknown{Name: method}
	} else if err := d.DecodeElement(msg, &start); err != nil {
		return fmt.Errorf("decode %s: %w", method, err)
	}
	setNamespace(env, start.Name.Space)
	env.Body = msg
	return nil
}

func setNamespace(env *Envelope, ns string) {
	if env.Namespace == "" && strings.HasPrefix(ns, cwmpNSPrefix) {
		env.Namespace = ns
	}
}

func parseBool(s string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(s))
	return err == nil && b
}

// Encode writes a SOAP envelope with the conventional soap-env, soap-enc,
// xsd, xsi and cwmp prefixes that CPE stacks expect.
func Encode(env *Envelope) ([]byte, error) {
	if env.Body == nil {
		return nil, errors.New("encode envelope: no message")
	}
	ns := env.Namespace
	if ns == "" {
		ns = NSCwmp10
	}
	buf := &bytes.Buffer{}
	buf.WriteString(xml.Header)
	fmt.Fprintf(buf, `<soap-env:Envelope xmlns:soap-env="%s" xmlns:soap-enc="%s" xmlns:xsd="%s" xmlns:xsi="%s" xmlns:cwmp="%s">`,
		NSSoapEnv, NSSoapEnc, NSXSD, NSXSI, ns)
	buf.WriteString(`<soap-env:Header>`)
	buf.WriteString(`<cwmp:ID soap-env:mustUnderstand="1">`)
	if err := xml.EscapeText(buf, []byte(env.ID)); err != nil {
		return nil, err
	}
	buf.WriteString(`</cwmp:ID>`)
	if env.HoldRequests {
		buf.WriteString(`<cwmp:HoldRequests soap-env:mustUnderstand="1">1</cwmp:HoldRequests>`)
	}
	buf.WriteString(`</soap-env:Header><soap-env:Body>`)

	name := "cwmp:" + env.Body.Method()
	if _, ok := env.Body.(*Fault); ok {
		name = "soap-env:Fault"
	}
	if _, ok := env.Body.(*Unknown); ok {
		return nil, fmt.Errorf("encode envelope: cannot encode unknown method %s", env.Body.Method())
	}
	enc := xml.NewEncoder(buf)
	if err := enc.EncodeElement(env.Body, xml.StartElement{Name: xml.Name{Local: name}}); err != nil {
		return nil, fmt.Errorf("encode %s: %w", env.Body.Method(), err)
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	buf.WriteString(`</soap-env:Body></soap-env:Envelope>`)
	return buf.Bytes(), nil
}

// marshalArray writes a SOAP-encoded array with its soap-enc:arrayType, which
// several CPE stacks require on the ParameterList and ParameterNames arrays.
func marshalArray[T any](e *xml.Encoder, start xml.StartElement, itemName, itemType string, items []T) error {
	start.Attr = append(start.Attr, xml.Attr{
		Name:  xml.Name{Local: "soap-enc:arrayType"},
		Value: fmt.Sprintf("%s[%d]", itemType, len(items)),
	})
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	for _, item := range items {
		if err := e.EncodeElement(item, xml.StartElement{Name: xml.Name{Local: itemName}}); err != nil {
			return err
		}
	}
	return e.EncodeToken(start.End())
}

// unmarshalArray decodes every child element as an item, whatever its name,
// since CPEs disagree on the item element names of SOAP arrays.
func unmarshalArray[T any](d *xml.Decoder, out *[]T) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			var item T
			if err := d.DecodeElement(&item, &t); err != nil {
				return err
			}
			*out = append(*out, item)
		case xml.EndElement:
			return nil
		}
	}
}

// charsetReader accepts the non-UTF-8 charsets seen in CPE stacks. ASCII is a
// subset of UTF-8; ISO-8859-1 maps byte for byte onto the first 256 runes.
func charsetReader(label string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(label) {
	case "utf-8", "utf8", "us-ascii", "ascii":
		return input, nil
	case "iso-8859-1", "latin1", "iso8859-1":
		raw, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		out := make([]byte, 0, len(raw))
		for _, b := range raw {
			out = utf8.AppendRune(out, rune(b))
		}
		return bytes.NewReader(out), nil
	}
	return nil, fmt.Errorf("unsupported charset %q", label)
}
