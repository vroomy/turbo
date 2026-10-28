// Package request parses incoming HTTP/1.x requests.
package request

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Parse reads a request, reusing r if it is already a buffered reader. The body
// must be read or closed before reading another request from the same reader.
// Request fields own their storage and remain valid when the reader is reused.
// Bodyless HTTP/1.1 GET and HEAD requests use an allocation-saving fast path;
// other requests use http.ReadRequest.
// Pass a *bufio.Reader to preserve buffered bytes across successive calls.
func Parse(r io.Reader) (*http.Request, error) {
	return parse(r, nil)
}

// Parser reuses request, URL, and header storage. Its zero value is ready to use.
// Small header strings share immutable 1 KiB blocks, which retained clones can
// keep alive.
// A Parser belongs to one worker and must not be copied or used concurrently
// after its first use.
type Parser struct {
	req        http.Request
	url        url.URL
	headers    http.Header
	values     []string
	headerText strings.Builder
}

const (
	maxRetainedHeaders  = 128
	headerTextBlockSize = 1024
	maxSharedHeaderSize = headerTextBlockSize / 4
)

// Parse reads a request using reusable storage where supported. The request and
// its metadata are valid until Reset or the next Parse. Use Request.Clone to
// retain metadata; cloning does not copy the body. Read or close the previous
// body before calling Parse again.
func (p *Parser) Parse(r io.Reader) (*http.Request, error) {
	p.Reset()
	return parse(r, p)
}

// Reset clears request fields while retaining bounded parsing storage. It does
// not close the body, which remains the caller's responsibility.
func (p *Parser) Reset() {
	p.req = http.Request{}
	p.url = url.URL{}
	if len(p.headers) > maxRetainedHeaders {
		p.headers = nil
	} else {
		clear(p.headers)
	}
	if cap(p.values) > maxRetainedHeaders {
		p.values = nil
	} else {
		clear(p.values)
		p.values = p.values[:0]
	}
}

func parse(r io.Reader, storage *Parser) (*http.Request, error) {
	reader, ok := r.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(r)
	}
	if _, err := reader.Peek(1); err != nil {
		return nil, err
	}
	buffer, _ := reader.Peek(reader.Buffered())
	if !bytes.HasPrefix(buffer, []byte("GET ")) && !bytes.HasPrefix(buffer, []byte("HEAD ")) {
		return http.ReadRequest(reader)
	}
	end := bytes.Index(buffer, []byte("\r\n\r\n"))
	if end >= 0 {
		if req, ok := parseBuffered(buffer[:end+2], storage); ok {
			_, _ = reader.Discard(end + 4)
			return req, nil
		}
	}
	// Nothing has been consumed. Keep the standard parser's validation and
	// body handling for fragmented headers and less common request forms.
	if storage != nil {
		storage.Reset()
	}
	return http.ReadRequest(reader)
}

// Allocating Parse keeps the request and URL in one allocation.
type message struct {
	req http.Request
	url url.URL
}

func parseBuffered(header []byte, storage *Parser) (*http.Request, bool) {
	line, fields, _ := bytes.Cut(header, []byte("\r\n"))
	methodBytes, rest, ok := bytes.Cut(line, []byte(" "))
	if !ok {
		return nil, false
	}
	var method string
	switch string(methodBytes) {
	case http.MethodGet:
		method = http.MethodGet
	case http.MethodHead:
		method = http.MethodHead
	default:
		return nil, false
	}
	targetBytes, proto, ok := bytes.Cut(rest, []byte(" "))
	if !ok || string(proto) != "HTTP/1.1" {
		return nil, false
	}
	// One owned string holds the target and fields. Method and protocol use
	// constants; no returned strings refer to the reader's mutable buffer.
	owned := ownHeader(targetBytes, fields, storage)
	target := owned[:len(targetBytes)]
	headers, host, ok := parseHeaders(owned[len(targetBytes):], storage)
	if !ok {
		return nil, false
	}
	req, err := makeRequest(target, storage)
	if err != nil {
		return nil, false
	}
	*req = http.Request{
		Method: method, RequestURI: target, Proto: "HTTP/1.1",
		ProtoMajor: 1, ProtoMinor: 1, URL: req.URL,
		Header: headers, Host: host,
		Body: http.NoBody, Close: connectionClose(headers.Get("Connection")),
	}
	if req.URL.Host != "" {
		req.Host = req.URL.Host
	}
	return req, true
}

func makeRequest(target string, storage *Parser) (*http.Request, error) {
	if simpleTarget([]byte(target)) {
		if storage != nil {
			storage.url = parseURL(target)
			storage.req.URL = &storage.url
			return &storage.req, nil
		}
		m := &message{url: parseURL(target)}
		m.req.URL = &m.url
		return &m.req, nil
	}
	u, err := url.ParseRequestURI(target)
	if err != nil {
		return nil, err
	}
	if storage != nil {
		storage.url = *u
		storage.req.URL = &storage.url
		return &storage.req, nil
	}
	return &http.Request{URL: u}, nil
}

func ownHeader(target, fields []byte, storage *Parser) string {
	size := len(target) + len(fields)
	if storage != nil && size <= maxSharedHeaderSize {
		text := &storage.headerText
		if text.Cap()-text.Len() < size {
			// Reset drops the old block. Retained strings keep it alive, and
			// previously written bytes are never overwritten.
			text.Reset()
			text.Grow(headerTextBlockSize)
		}
		start := text.Len()
		text.Write(target)
		text.Write(fields)
		return text.String()[start:]
	}

	// Large headers own their allocation without displacing the shared block.
	var text strings.Builder
	text.Grow(size)
	text.Write(target)
	text.Write(fields)
	return text.String()
}

// Handle unescaped origin-form targets; net/url handles other URL forms.
func parseURL(target string) url.URL {
	path, query, hasQuery := strings.Cut(target, "?")
	return url.URL{Path: path, RawQuery: query, ForceQuery: hasQuery && query == ""}
}

func simpleTarget(target []byte) bool {
	if len(target) == 0 || target[0] != '/' {
		return false
	}
	path, query, _ := bytes.Cut(target, []byte("?"))
	for _, c := range path {
		if !pathBytes[c] {
			return false
		}
	}
	for _, c := range query {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

func parseHeaders(fields string, storage *Parser) (http.Header, string, bool) {
	// Host lives in Request.Host, so it needs no map entry or value slice.
	// Size fresh storage once. Reused storage usually has enough capacity; only
	// count the remaining lines if its value slice actually needs to grow.
	var count int
	if storage == nil || storage.headers == nil {
		count = max(strings.Count(fields, "\n")-1, 0)
	}
	headers, values := headerStorage(storage, count)
	if storage != nil {
		defer func() { storage.values = values }()
	}
	var host string
	var sawHost bool
	for fields != "" {
		line, rest, ok := strings.Cut(fields, "\r\n")
		if !ok {
			return nil, "", false
		}
		fields = rest
		key, value, ok := strings.Cut(line, ":")
		if !ok || !validValue(value) {
			return nil, "", false
		}
		key, ok = canonicalHeaderKey(key)
		if !ok {
			return nil, "", false
		}
		value = strings.Trim(value, " \t")
		switch key {
		case "Host":
			if sawHost {
				return nil, "", false
			}
			host, sawHost = value, true
			continue
		case "Transfer-Encoding", "Upgrade", "Pragma":
			return nil, "", false
		case "Content-Length":
			if value != "0" {
				return nil, "", false
			}
		}
		if _, duplicate := headers[key]; duplicate {
			return nil, "", false
		}
		if len(values) == cap(values) {
			remaining := strings.Count(fields, "\n")
			if !sawHost {
				// Reserve no value slot for a Host still to come, just as
				// fresh storage excludes Host from its initial count.
				remaining = max(remaining-1, 0)
			}
			grown := make([]string, len(values), len(values)+1+remaining)
			copy(grown, values)
			values = grown
		}
		values = append(values, value)
		// Cap each slice so a handler's append cannot overwrite another field.
		i := len(values) - 1
		headers[key] = values[i : i+1 : i+1]
	}
	return headers, host, true
}

func headerStorage(storage *Parser, count int) (http.Header, []string) {
	if storage == nil {
		return make(http.Header, count), make([]string, 0, count)
	}
	if storage.headers == nil {
		storage.headers = make(http.Header, count)
	}
	if cap(storage.values) < count {
		storage.values = make([]string, 0, count)
	}
	return storage.headers, storage.values[:0]
}

// Common spellings are already validated constants. For other fields, check
// token validity and canonical casing together, avoiding a second scan for
// canonical names. Noncanonical names try immutable common names before the
// standard canonicalizer.
func canonicalHeaderKey(s string) (string, bool) {
	switch s {
	case "Host", "host":
		return "Host", true
	case "Connection", "connection":
		return "Connection", true
	case "Content-Length", "content-length":
		return "Content-Length", true
	}
	if s == "" {
		return "", false
	}
	upper := true
	for i := range s {
		c := s[i]
		if !tokenBytes[c] {
			return "", false
		}
		if (upper && 'a' <= c && c <= 'z') || (!upper && 'A' <= c && c <= 'Z') {
			// Casing is already noncanonical. Only token validation remains
			// before checking common names or using the standard library.
			for j := i + 1; j < len(s); j++ {
				if !tokenBytes[s[j]] {
					return "", false
				}
			}
			return canonicalUncommonHeaderKey(s), true
		}
		upper = c == '-'
	}
	return s, true
}

// Only noncanonical, validated ASCII names reach here. In addition to the
// three framing names, recognize browser metadata and tracing names that
// net/textproto does not intern. Return immutable constants
// without retaining request substrings or adding per-worker cache storage.
func canonicalUncommonHeaderKey(s string) string {
	switch len(s) {
	case 4:
		if strings.EqualFold(s, "Host") {
			return "Host"
		}
	case 9:
		if strings.EqualFold(s, "Sec-Ch-Ua") {
			return "Sec-Ch-Ua"
		}
	case 10:
		if strings.EqualFold(s, "Connection") {
			return "Connection"
		}
		if strings.EqualFold(s, "Tracestate") {
			return "Tracestate"
		}
	case 11:
		if strings.EqualFold(s, "Traceparent") {
			return "Traceparent"
		}
	case 12:
		if strings.EqualFold(s, "X-Request-Id") {
			return "X-Request-Id"
		}
	case 14:
		// Several names share this length. Reject unrelated prefixes before
		// doing case-insensitive comparisons (for example custom X- names).
		switch s[0] {
		case 'C', 'c':
			if strings.EqualFold(s, "Content-Length") {
				return "Content-Length"
			}
		case 'S', 's':
			if strings.EqualFold(s, "Sec-Fetch-Dest") {
				return "Sec-Fetch-Dest"
			}
			if strings.EqualFold(s, "Sec-Fetch-Mode") {
				return "Sec-Fetch-Mode"
			}
			if strings.EqualFold(s, "Sec-Fetch-Site") {
				return "Sec-Fetch-Site"
			}
			if strings.EqualFold(s, "Sec-Fetch-User") {
				return "Sec-Fetch-User"
			}
		}
	case 16:
		if strings.EqualFold(s, "Sec-Ch-Ua-Mobile") {
			return "Sec-Ch-Ua-Mobile"
		}
		if strings.EqualFold(s, "X-Correlation-Id") {
			return "X-Correlation-Id"
		}
	case 18:
		if strings.EqualFold(s, "Sec-Ch-Ua-Platform") {
			return "Sec-Ch-Ua-Platform"
		}
	case 25:
		if strings.EqualFold(s, "Upgrade-Insecure-Requests") {
			return "Upgrade-Insecure-Requests"
		}
	}
	return http.CanonicalHeaderKey(s)
}

func validValue(s string) bool {
	for i := range s {
		c := s[i]
		if (c < 0x20 && c != '\t') || c >= 0x7f {
			return false
		}
	}
	return true
}

func connectionClose(value string) bool {
	for value != "" {
		var token string
		token, value, _ = strings.Cut(value, ",")
		if strings.EqualFold(strings.Trim(token, " \t"), "close") {
			return true
		}
	}
	return false
}

var tokenBytes = byteSet("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!#$%&'*+-.^_`|~")
var pathBytes = byteSet("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~$&+,;=:@/")

func byteSet(chars string) (set [256]bool) {
	for i := range chars {
		set[chars[i]] = true
	}
	return set
}
