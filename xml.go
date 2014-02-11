// Copyright 2012-2014 Charles Banning. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file

// xml.go - basically the core of X2j for map[string]interface{} values.
//          NewMapXml, NewMapXmlReader, mv.Xml, mv.XmlWriter
// see x2j and j2x for wrappers to provide end-to-end transformation of XML and JSON messages.

package mxj

import (
	// "fmt"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ------------------- NewMapXml & NewMapXmlReader ... from x2j2 -------------------------

// If XmlCharsetReader != nil, it will be used to decode the XML, if required.
//   import (
//	     charset "code.google.com/p/go-charset/charset"
//	     github.com/clbanning/mxj
//	 )
//   ...
//   mu.XmlCharsetReader = charset.NewReader
//   m, merr := mu.NewMapXml(xmlValue)
var XmlCharsetReader func(charset string, input io.Reader) (io.Reader, error)

// NewMapXml - convert an XML doc into a Map
// (This is analogous to unmarshalling a JSON string to map[string]interface{} using json.Unmarshal().)
//	If the optional argument 'recast' is 'true', then values will be converted to boolean or float64 if possible.
//
//	Converting XML to JSON is a simple as:
//		...
//		mapVal, merr := mxj.NewMapXml(xmlVal)
//		if merr != nil {
//			// handle error
//		}
//		jsonVal, jerr := mapVal.Json()
//		if jerr != nil {
//			// handle error
//		}
func NewMapXml(xmlVal []byte, recast ...bool) (Map, error) {
	var r bool
	if len(recast) == 1 {
		r = recast[0]
	}
	n, err := xmlToTree(xmlVal)
	if err != nil {
		return nil, err
	}

	m := make(map[string]interface{}, 0)
	m[n.key] = n.treeToMap(r)

	return m, nil
}

// Get next XML doc from an io.Reader as a Map value.  Returns Map value.
func NewMapXmlReader(xmlReader io.Reader, recast ...bool) (Map, error) {
	var r bool
	if len(recast) == 1 {
		r = recast[0]
	}

	// build the node tree
	n, err := xmlReaderToTree(xmlReader)
	if err != nil {
		return nil, err
	}

	// create the Map value
	m := make(map[string]interface{})
	m[n.key] = n.treeToMap(r)

	return m, nil
}

// Get next XML doc from an io.Reader as a Map value.  Returns Map value and pointer to raw XML.
//	NOTE: Due to the implementation of xml.Decoder, the raw XML off the reader is buffered to *[]byte
//	      using a ByteReader. If the io.Reader is an os.File, there may be significant performance impact.
//	      See the examples - getmetrics1.go through getmetrics4.go - for comparative use cases on a large
//	      data set. If the io.Reader is wrapping a []byte value in-memory, however, such as http.Request.Body
//	      you CAN use it to efficiently unmarhal an XML and retrieve the raw XML in a single call.
func NewMapXmlReaderRaw(xmlReader io.Reader, recast ...bool) (Map, *[]byte, error) {
	var r bool
	if len(recast) == 1 {
		r = recast[0]
	}
	// create TeeReader so we can retrieve raw XML
	wb := new(bytes.Buffer)
	trdr := myTeeReader(xmlReader, wb) // see code at EOF

	// build the node tree
	n, err := xmlReaderToTree(trdr)

	// retrieve the raw XML that was decoded
	b := make([]byte, wb.Len())
	_, _ = wb.Read(b)

	if err != nil {
		return nil, &b, err
	}

	// create the Map value
	m := make(map[string]interface{})
	m[n.key] = n.treeToMap(r)

	return m, &b, nil
}

// xmlReaderToTree() - parse a XML io.Reader to a tree of nodes
func xmlReaderToTree(rdr io.Reader) (*node, error) {
	// parse the Reader
	p := xml.NewDecoder(rdr)
	p.CharsetReader = XmlCharsetReader
	return xmlToTreeParser("", nil, p)
}

// for building the parse tree
type node struct {
	dup   bool   // is member of a list
	attr  bool   // is an attribute
	key   string // XML tag
	val   string // element value
	nodes []*node
}

// xmlToTree - convert an XML doc into a tree of nodes.
func xmlToTree(doc []byte) (*node, error) {
	// xml.Decoder doesn't properly handle whitespace in some doc
	// see songTextString.xml test case ...
	reg, _ := regexp.Compile("[ \t\n\r]*<")
	doc = reg.ReplaceAll(doc, []byte("<"))

	b := bytes.NewBuffer(doc)
	p := xml.NewDecoder(b)
	p.CharsetReader = XmlCharsetReader
	n, berr := xmlToTreeParser("", nil, p)
	if berr != nil {
		return nil, berr
	}

	return n, nil
}

// xmlToTreeParser - load a 'clean' XML doc into a tree of *node.
func xmlToTreeParser(skey string, a []xml.Attr, p *xml.Decoder) (*node, error) {
	n := new(node)
	n.nodes = make([]*node, 0)

	if skey != "" {
		n.key = skey
		if len(a) > 0 {
			for _, v := range a {
				na := new(node)
				na.attr = true
				na.key = `-` + v.Name.Local
				na.val = v.Value
				n.nodes = append(n.nodes, na)
			}
		}
	}
	for {
		t, err := p.Token()
		if err != nil {
			return nil, err
		}
		switch t.(type) {
		case xml.StartElement:
			tt := t.(xml.StartElement)
			// handle root
			if n.key == "" {
				n.key = tt.Name.Local
				if len(tt.Attr) > 0 {
					for _, v := range tt.Attr {
						na := new(node)
						na.attr = true
						na.key = `-` + v.Name.Local
						na.val = v.Value
						n.nodes = append(n.nodes, na)
					}
				}
			} else {
				nn, nnerr := xmlToTreeParser(tt.Name.Local, tt.Attr, p)
				if nnerr != nil {
					return nil, nnerr
				}
				n.nodes = append(n.nodes, nn)
			}
		case xml.EndElement:
			// scan n.nodes for duplicate n.key values
			n.markDuplicateKeys()
			return n, nil
		case xml.CharData:
			tt := string(t.(xml.CharData))
			// clean up possible noise
			tt = strings.Trim(tt,"\t\r\b\n ")
			if len(n.nodes) > 0 && len(tt) > 0 {
			// if len(n.nodes) > 0 {
				nn := new(node)
				nn.key = "#text"
				nn.val = tt
				n.nodes = append(n.nodes, nn)
			} else {
				n.val = tt
			}
		default:
			// noop
		}
	}
	// Logically we can't get here, but provide an error message anyway.
	return nil, errors.New("Unknown parse error in xmlToTree() for: " + n.key)
}

// (*node)markDuplicateKeys - set node.dup flag for loading map[string]interface{}.
func (n *node) markDuplicateKeys() {
	l := len(n.nodes)
	for i := 0; i < l; i++ {
		if n.nodes[i].dup {
			continue
		}
		for j := i + 1; j < l; j++ {
			if n.nodes[i].key == n.nodes[j].key {
				n.nodes[i].dup = true
				n.nodes[j].dup = true
			}
		}
	}
}

// (*node)treeToMap - convert a tree of nodes into a map[string]interface{}.
//	(Parses to map that is structurally the same as from json.Unmarshal().)
// Note: root is not instantiated; call with: "m[n.key] = n.treeToMap(recast)".
func (n *node) treeToMap(r bool) interface{} {
	if len(n.nodes) == 0 {
		return recast(n.val, r)
	}

	m := make(map[string]interface{}, 0)
	for _, v := range n.nodes {
		// just a value
		if !v.dup && len(v.nodes) == 0 {
			m[v.key] = recast(v.val, r)
			continue
		}

		// a list of values
		if v.dup {
			var a []interface{}
			if vv, ok := m[v.key]; ok {
				a = vv.([]interface{})
			} else {
				a = make([]interface{}, 0)
			}
			a = append(a, v.treeToMap(r))
			m[v.key] = interface{}(a)
			continue
		}

		// it's a unique key
		m[v.key] = v.treeToMap(r)
	}

	return interface{}(m)
}

// recast - try to cast string values to bool or float64
func recast(s string, r bool) interface{} {
	if r {
		// handle numeric strings ahead of boolean
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return interface{}(f)
		}
		// ParseBool treats "1"==true & "0"==false
		if b, err := strconv.ParseBool(s); err == nil {
			return interface{}(b)
		}
	}
	return interface{}(s)
}

// ------------------ END: NewMapXml & NewMapXmlReader -------------------------

// ------------------ mv.Xml & mv.XmlWriter - from j2x ------------------------

const (
	DefaultRootTag = "doc"
)

var useGoXmlEmptyElemSyntax bool

// XmlGoEmptyElemSyntax() - <tag ...></tag> rather than <tag .../>.
//	Go's encoding/xml package marshals empty XML elements as <tag ...></tag>.  By default this package
//	encodes empty elements as <tag .../>.  If you're marshaling Map values that include structures 
//	(which are passed to xml.Marshal for encoding), this will let you conform to the standard package.
//	Alternatively, you can replace the encoding/xml/marshal.go file in the standard libary with the 
//	patched version in the "xml_marshal" folder in this package.
func XmlGoEmptyElemSyntax() {
	useGoXmlEmptyElemSyntax = true
}

// XmlDefaultEmptyElemSyntax() - <tag .../> rather than <tag ...></tag>.
// Return XML encoding for empty elements to the default package setting.
// Reverses effect of XmlGoEmptyElemSyntax().
func XmlDefaultEmptyElemSyntax() {
	useGoXmlEmptyElemSyntax = false
}

// Encode a Map as XML.  The companion of NewMapXml().
// The following rules apply.
//    - The key label "#text" is treated as the value for a simple element with attributes.
//    - Map keys that begin with a hyphen, '-', are interpreted as attributes.
//      It is an error if the attribute doesn't have a []byte, string, number, or boolean value.
//    - Map value type encoding:
//          > string, bool, float64, int, int32, int64, float32: per "%v" formating
//          > []bool, []uint8: by casting to string
//          > structures, etc.: handed to xml.Marshal() - if there is an error, the element
//            value is "UNKNOWN"
//    - Elements with only attribute values or are null are terminated using "/>".
//    - If len(m) == 1 and no rootTag is provided, then the map key is used as the root tag.
//      Thus, `{ "key":"value" }` encodes as "<key>value</key>".
//    - To encode empty elements in a syntax consistent with encoding/xml call UseGoXmlEmptyElementSyntax().
func (mv Map) Xml(rootTag ...string) ([]byte, error) {
	m := map[string]interface{}(mv)
	var err error
	s := new(string)

	if len(m) == 1 && len(rootTag) == 0 {
		for key, value := range m {
			if _, ok := value.([]interface{}); ok {
				err = mapToXml(s, DefaultRootTag, m)
			} else {
				err = mapToXml(s, key, value)
			}
		}
	} else if len(rootTag) == 1 {
		err = mapToXml(s, rootTag[0], m)
	} else {
		err = mapToXml(s, DefaultRootTag, m)
	}
	return []byte(*s), err
}

// The following implementation is provided only for symmetry with NewMapXmlReader[Raw]
// The names will also provide a key for the number of return arguments.

// Writes the Map as  XML on the Writer. 
// See Xml() for encoding rules.
func (mv Map) XmlWriter(xmlWriter io.Writer, rootTag ...string) error {
	x, err := mv.Xml(rootTag...)
	if err != nil {
		return err
	}

	_, err = xmlWriter.Write(x)
	return err
}

// Writes the Map as  XML on the Writer. *[]byte is the raw XML that was written.
// See Xml() for encoding rules.
func (mv Map) XmlWriterRaw(xmlWriter io.Writer, rootTag ...string) (*[]byte, error) {
	x, err := mv.Xml(rootTag...)
	if err != nil {
		return &x, err
	}

	_, err = xmlWriter.Write(x)
	return &x, err
}

// where the work actually happens
// returns an error if an attribute is not atomic
func mapToXml(s *string, key string, value interface{}) error {
	var endTag bool

	if _, ok := value.([]interface{}); !ok {
		*s += `<` + key
	}
	switch value.(type) {
	case map[string]interface{}:
		vv := value.(map[string]interface{})
		lenvv := len(vv)
		// scan out attributes - keys have prepended hyphen, '-'
		var cntAttr int
		for k, v := range vv {
			if k[:1] == "-" {
				switch v.(type) {
				case string, float64, bool, int, int32, int64, float32:
					*s += ` ` + k[1:] + `="` + fmt.Sprintf("%v", v) + `"`
					cntAttr++
				case []byte: // allow standard xml pkg []byte transform, as below
					*s += ` ` + k[1:] + `="` + fmt.Sprintf("%v", string(v.([]byte))) + `"`
					cntAttr++
				default:
					return errors.New("invalid attribute value for: " + k)
				}
			}
		}
		// only attributes?
		if cntAttr == lenvv {
			break
		}
		// simple element? Note: '#text" is an invalid XML tag.
		if v, ok := vv["#text"]; ok {
			if cntAttr+1 < lenvv {
				return errors.New("#text key occurs with other non-attribute keys")
			}
			*s += ">" + fmt.Sprintf("%v", v)
			endTag = true
			break
		}
		// close tag with possible attributes
		*s += ">"
		// something more complex
		for k, v := range vv {
			if k[:1] == "-" {
				continue
			}
			mapToXml(s, k, v)
		}
		endTag = true
	case []interface{}:
		for _, v := range value.([]interface{}) {
			mapToXml(s, key, v)
		}
		return nil
	case nil:
		// terminate the tag
		break
	default: // handle anything - even goofy stuff
		var tmp string
		switch value.(type) {
		case string, float64, bool, int, int32, int64, float32:
			tmp = fmt.Sprintf("%v", value)
		case []byte: // NOTE: byte is just an alias for uint8
			// similar to how xml.Marshal handles []byte structure members
			tmp = fmt.Sprintf("%v", string(value.([]byte)))
		default:
			v, err := xml.Marshal(value)
			if err != nil {
				tmp = "UNKNOWN"
			} else {
				tmp = string(v)
			}
		}
		if len(tmp) > 0 {
			*s += ">" + tmp
			endTag = true
		}
	}

	if endTag {
		*s += "</" + key + ">"
	} else if useGoXmlEmptyElemSyntax {
		*s += "></" + key + ">"
	} else {
		*s += "/>"
	}
	return nil
}

// -------------------- END: mv.Xml & mv.XmlWriter -------------------------------

// --------------  Handle XML stream by processing Map value --------------------

// Default poll delay to keep Handler from spinning on an open stream
// like sitting on os.Stdin waiting for imput.
var xhandlerPollInterval = time.Duration(1e6)

// Bulk process XML using handlers that process a Map value.
//	'rdr' is an io.Reader for XML (stream)
//	'mapHandler' is the Map processing handler. Return of 'false' stops further processing.
//	'errHandler' is the error processing handler. Return of 'false' stops further processing and returns the error.
//	Note: mapHandler() and errHandler() calls are blocking, so reading and processing of messages is serialized.
//	      This means that you can stop reading the file on error or after processing a particular message.
//	      To have reading and handling run concurrently, pass argument(s) to a go routine in handler and return true.
func HandleXmlReader(xmlReader io.Reader, mapHandler func(Map) bool, errHandler func(error) bool) error {
	var n int
	for {
		m, merr := NewMapXmlReader(xmlReader)
		n++

		// handle error condition with errhandler
		if merr != nil && merr != io.EOF {
			merr = errors.New(fmt.Sprintf("[xmlReader: %d] %s", n, merr.Error()))
			if ok := errHandler(merr); !ok {
				// caused reader termination
				return merr
			}
			continue
		}

		// pass to maphandler
		if len(m) != 0 {
			if ok := mapHandler(m); !ok {
				break
			}
		} else if merr != io.EOF {
			<-time.After(xhandlerPollInterval)
		}

		if merr == io.EOF {
			break
		}
	}
	return nil
}

// Bulk process XML using handlers that process a Map value and the raw XML.
//	'rdr' is an io.Reader for XML (stream)
//	'mapHandler' is the Map and raw XML - *[]byte - processing handler. Return of 'false' stops further processing.
//	'errHandler' is the error and raw XML processing handler. Return of 'false' stops further processing and returns the error.
//	Note: mapHandler() and errHandler() calls are blocking, so reading and processing of messages is serialized.
//	      This means that you can stop reading the file on error or after processing a particular message.
//	      To have reading and handling run concurrently, pass argument(s) to a go routine in handler and return true.
//	See NewMapXmlReaderRaw for comment on performance associated with retrieving raw XML from a Reader.
func HandleXmlReaderRaw(xmlReader io.Reader, mapHandler func(Map, *[]byte) bool, errHandler func(error, *[]byte) bool) error {
	var n int
	for {
		m, raw, merr := NewMapXmlReaderRaw(xmlReader)
		n++

		// handle error condition with errhandler
		if merr != nil && merr != io.EOF {
			merr = errors.New(fmt.Sprintf("[xmlReader: %d] %s", n, merr.Error()))
			if ok := errHandler(merr, raw); !ok {
				// caused reader termination
				return merr
			}
			continue
		}

		// pass to maphandler
		if len(m) != 0 {
			if ok := mapHandler(m, raw); !ok {
				break
			}
		} else if merr != io.EOF {
			<-time.After(xhandlerPollInterval)
		}

		if merr == io.EOF {
			break
		}
	}
	return nil
}

// ----------------- END: Handle XML stream by processing Map value --------------

// --------  a hack of io.TeeReader ... need one thats an io.ByteReader for xml.NewDecoder() ----------

// This is a clone of io.TeeReader with the addition method t.ReadByte().
// Thus, this TeeReader is also an io.ByteReader.
// This is necssary because xml.NewDecoder uses a ByteReader not a Reader.
// If NewDecoder is passed a Reader that does not satisfy ByteReader it wraps the Reader with
// bufio.NewReader and uses ReadByte rather than Read that runs the TeeReader pipe logic.

type teeReader struct {
	r io.Reader
	w io.Writer
	b []byte
}

func myTeeReader(r io.Reader, w io.Writer) io.Reader {
	b := make([]byte, 1)
	return &teeReader{r, w, b}
}

// need for io.Reader - but we don't use it ... 
func (t *teeReader) Read(p []byte) (n int, err error) {
	return 0, nil
}

func (t *teeReader) ReadByte() (c byte, err error) {
	n, err := t.r.Read(t.b)
	if n > 0 {
		if _, err := t.w.Write(t.b[:1]); err != nil {
			return t.b[0], err
		}
	}
	return t.b[0], err
}

// ----------------------- END: io.TeeReader hack -----------------------------------

// ---------------------- XmlIndent - from j2x package ----------------------------

// Encode a map[string]interface{} as a pretty XML string.
// See Xml for encoding rules.
func (mv Map)XmlIndent(prefix, indent string, rootTag ...string) ([]byte, error) {
	m := map[string]interface{}(mv)

	var err error
	s := new(string)
	p := new(pretty)
	p.indent = indent
	p.padding = prefix

	if len(m) == 1 && len(rootTag) == 0 {
		for key, value := range m {
			if _, ok := value.([]interface{}); ok {
				err = p.mapToXmlIndent(s, DefaultRootTag, m)
			} else {
				err = p.mapToXmlIndent(s, key, value)
			}
		}
	} else if len(rootTag) == 1 {
		err = p.mapToXmlIndent(s, rootTag[0], m)
	} else {
		err = p.mapToXmlIndent(s, DefaultRootTag, m)
	}
	return []byte(*s), err
}

type pretty struct {
	indent string
	cnt int
	padding string
	inList bool
	inMap bool
}

func (p *pretty)Indent() {
	p.padding += p.indent
	p.cnt++
}

func (p *pretty)Dedent() {
	if p.cnt > 0 {
		p.padding = p.padding[:len(p.padding)-len(p.indent)]
		p.cnt--
	}
}

// where the work actually happens
// returns an error if an attribute is not atomic
func (p *pretty)mapToXmlIndent(s *string, key string, value interface{}) error {
	var endTag bool
	var isSimple bool

	switch value.(type) {
	case map[string]interface{}, []byte, string, float64, bool, int, int32, int64, float32:
		*s += p.padding + `<` + key
	}
	switch value.(type) {
	case map[string]interface{}:
		vv := value.(map[string]interface{})
		lenvv := len(vv)
		// scan out attributes - keys have prepended hyphen, '-'
		var cntAttr int
		for k, v := range vv {
			if k[:1] == "-" {
				switch v.(type) {
				case string, float64, bool, int, int32, int64, float32:
					*s += ` ` + k[1:] + `="` + fmt.Sprintf("%v", v) + `"`
					cntAttr++
				case []byte: // allow standard xml pkg []byte transform, as below
					*s += ` ` + k[1:] + `="` + fmt.Sprintf("%v", string(v.([]byte))) + `"`
					cntAttr++
				default:
					return errors.New("invalid attribute value for: " + k)
				}
			}
		}
		// only attributes?
		if cntAttr == lenvv {
			break
		}
		// simple element? Note: '#text" is an invalid XML tag.
		if v, ok := vv["#text"]; ok {
			if cntAttr+1 < lenvv {
				return errors.New("#text key occurs with other non-attribute keys")
			}
			*s += ">" + fmt.Sprintf("%v", v)
			endTag = true
			break
		}
		// close tag with possible attributes
		*s += ">"
		*s += "\n"
		// something more complex
		p.inMap = true
		for k, v := range vv {
			if k[:1] == "-" {
				continue
			}
			switch v.(type) {
			case []interface{}:
			default:
				p.Indent()
			}
			p.mapToXmlIndent(s, k, v)
			switch v.(type) {
			case []interface{}:	// handled in []interface{} case
			default:
				if !p.inList { p.Dedent() }
			}
		}
		p.inMap = false
		endTag = true
	case []interface{}:
		p.inList = true
		for _, v := range value.([]interface{}) {
			p.Indent()
			p.mapToXmlIndent(s, key, v)
			p.Dedent()
		}
		p.inList = false
		return nil
	case nil:
		// terminate the tag
		break
	default: // handle anything - even goofy stuff
		switch value.(type) {
		case string, float64, bool, int, int32, int64, float32:
			*s += ">" + fmt.Sprintf("%v", value)
		case []byte: // NOTE: byte is just an alias for uint8
			// similar to how xml.Marshal handles []byte structure members
			*s += ">" + fmt.Sprintf("%v", string(value.([]byte)))
		default:
			var v []byte
			var err error
				v, err = xml.MarshalIndent(value,p.padding,p.indent)
			if err != nil {
				*s += ">UNKNOWN"
			} else {
				*s += string(v)
			}
		}
		isSimple = true
		endTag = true
	}

	if endTag {
		if !isSimple {
			if p.inList { p.Dedent() }
			*s += p.padding
		}
		switch value.(type) {
		case map[string]interface{}, []byte, string, float64, bool, int, int32, int64, float32:
			*s += `</` + key + ">"
		}
		// *s += "</" + key + ">"
	} else {
		*s += "/>"
	}
	*s += "\n"
	if !p.inList && !p.inMap {
		p.Dedent()
	}

	return nil
}

