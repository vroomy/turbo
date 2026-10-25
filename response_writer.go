package turbo

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"
)

var _ http.ResponseWriter = &responseWriter{}

const maxRetainedHeaderCapacity = 16 << 10

// Copy only small first writes into the worker's header buffer. Larger bodies
// go straight to the connection, and every Write still sends immediately.
const maxCoalescedResponseSize = 4 << 10

var responseHeaderExclusions = map[string]bool{
	"Connection":        true,
	"Content-Length":    true,
	"Transfer-Encoding": true,
	"Trailer":           true,
}

var informationalHeaderExclusions = map[string]bool{
	"Connection":        true,
	"Content-Length":    true,
	"Date":              true,
	"Transfer-Encoding": true,
	"Trailer":           true,
}

var bodylessHeaderExclusions = map[string]bool{
	"Connection":        true,
	"Content-Length":    true,
	"Content-Type":      true,
	"Transfer-Encoding": true,
	"Trailer":           true,
}

func makeResponseWriter() (w responseWriter) {
	w.headers = make(http.Header)
	w.headerBuffer = new(bytes.Buffer)
	w.populateHeaders()
	return w
}

type responseWriter struct {
	headers http.Header
	c       net.Conn

	headerBuffer *bytes.Buffer

	// Worker-local, immutable Date value retained across responses. An empty
	// string marks an uninitialized cache, including at Unix second zero.
	dateSecond int64
	dateValue  string

	sentHeader       bool
	sniffContentType bool

	statusCode int

	contentLength int64
	written       int64

	head bool
	err  error
}

func (r *responseWriter) populateHeaders() {
	for key := range r.headers {
		if key != "Connection" && key != "Date" {
			delete(r.headers, key)
		}
	}
	r.populateHeader("Connection", "close")
	r.populateHeader("Date", r.dateHeader(time.Now()))
	r.contentLength = -1
}

func (r *responseWriter) dateHeader(now time.Time) string {
	second := now.Unix()
	if r.dateValue == "" || r.dateSecond != second {
		r.dateSecond = second
		r.dateValue = now.UTC().Format(http.TimeFormat)
	}
	return r.dateValue
}

func (r *responseWriter) populateHeader(key, value string) {
	values := r.headers[key]
	if len(values) == 1 && values[0] == value {
		return
	}
	r.headers.Set(key, value)
}

func (r *responseWriter) reset() {
	buffer := r.headerBuffer
	if buffer.Cap() > maxRetainedHeaderCapacity {
		buffer = new(bytes.Buffer)
	} else {
		buffer.Reset()
	}
	// Retain worker storage and the Date cache while clearing request state.
	*r = responseWriter{
		headers: r.headers, headerBuffer: buffer,
		dateSecond: r.dateSecond, dateValue: r.dateValue,
	}
}

func (r *responseWriter) Header() http.Header {
	return r.headers
}

func (r *responseWriter) Write(bs []byte) (n int, err error) {
	if r.err != nil {
		return 0, r.err
	}

	if len(bs) == 0 {
		return 0, nil
	}

	if err = r.ensureHeader(); err != nil {
		return 0, err
	}

	r.appendContentType(bs)

	if !r.sentHeader && !r.head && len(bs) <= maxCoalescedResponseSize-r.headerBuffer.Len()-2 &&
		r.validateStatusCodeForWriting() == nil && r.validateContentLength(bs) == nil {
		headerLength := r.headerBuffer.Len() + 2
		n, err = r.writeHeaderAndBody(bs)
		// The connection counts header bytes too, including on a partial write.
		n = max(0, n-headerLength)
		r.written += int64(n)
		return n, err
	}

	if _, err = r.writeHeader(); err != nil {
		return 0, err
	}

	return r.writeBody(bs)
}

func (r *responseWriter) WriteHeader(statusCode int) {
	if r.statusCode != 0 || r.err != nil {
		return
	}

	if statusCode < 100 || statusCode > 999 {
		log.Printf("invalid WriteHeader code %d\n", statusCode)
		return
	}

	r.statusCode = statusCode

	// Serialize the committed fields once so later map changes cannot affect the
	// response. Content-Type can still be inferred before sending the header.
	r.err = r.prepareHeader()
	if r.isInformational() {
		r.writeInformationalHeader()
	}
}

func (r *responseWriter) ensureHeader() error {
	if r.statusCode == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return r.err
}

func (r *responseWriter) prepareHeader() (err error) {
	if !r.isInformational() {
		if err = r.readContentLength(); err != nil {
			return err
		}
		r.sniffContentType = r.shouldSniffContentType()
	}

	r.appendStatusLine()
	if err = r.headers.WriteSubset(r.headerBuffer, r.headerExclusions()); err != nil {
		return err
	}
	if !r.isInformational() {
		r.appendFramingHeaders()
	}
	return nil
}

func (r *responseWriter) isInformational() bool {
	return r.statusCode < 200 && r.statusCode != http.StatusSwitchingProtocols
}

func (r *responseWriter) headerExclusions() map[string]bool {
	switch {
	case r.isInformational():
		return informationalHeaderExclusions
	case r.validateStatusCodeForWriting() != nil:
		return bodylessHeaderExclusions
	default:
		return responseHeaderExclusions
	}
}

func (r *responseWriter) readContentLength() (err error) {
	r.contentLength = -1
	switch {
	case r.statusCode < 200:
		return nil
	case r.statusCode == http.StatusNoContent:
		return nil
	}

	values := r.headers.Values("Content-Length")
	if len(values) == 0 {
		return nil
	}

	r.contentLength, err = strconv.ParseInt(values[0], 10, 64)
	if err != nil || r.contentLength < 0 || len(values) != 1 {
		return fmt.Errorf("turbo: invalid Content-Length %q", values)
	}
	return nil
}

func (r *responseWriter) shouldSniffContentType() bool {
	if r.validateStatusCodeForWriting() != nil {
		return false
	}
	_, set := r.headers["Content-Type"]
	return !set && r.headers.Get("Content-Encoding") == ""
}

func (r *responseWriter) appendStatusLine() {
	r.headerBuffer.WriteString("HTTP/1.1 ")
	r.appendInt(int64(r.statusCode))
	r.headerBuffer.WriteByte(' ')
	text := http.StatusText(r.statusCode)
	if text == "" {
		r.headerBuffer.WriteString("status code ")
		r.appendInt(int64(r.statusCode))
	} else {
		r.headerBuffer.WriteString(text)
	}
	r.headerBuffer.WriteString("\r\n")
}

func (r *responseWriter) appendInt(value int64) {
	var digits [20]byte
	r.headerBuffer.Write(strconv.AppendInt(digits[:0], value, 10))
}

func (r *responseWriter) appendFramingHeaders() {
	// Each connection serves one request, so streaming bodies end at EOF.
	r.headerBuffer.WriteString("Connection: close\r\n")
	if r.contentLength < 0 {
		return
	}
	r.headerBuffer.WriteString("Content-Length: ")
	r.appendInt(r.contentLength)
	r.headerBuffer.WriteString("\r\n")
}

func (r *responseWriter) appendContentType(bs []byte) {
	if r.sentHeader {
		return
	}

	if !r.sniffContentType {
		return
	}

	r.headerBuffer.WriteString("Content-Type: ")
	r.headerBuffer.WriteString(http.DetectContentType(bs))
	r.headerBuffer.WriteString("\r\n")
	r.sniffContentType = false
}

func (r *responseWriter) writeInformationalHeader() {
	_, _ = r.writeHeader()
	r.headerBuffer.Reset()
	r.statusCode = 0
	r.sentHeader = false
}

func (r *responseWriter) writeHeader() (n int, err error) {
	return r.writeHeaderAndBody(nil)
}

// bs is either nil or a validated, bounded first body write.
func (r *responseWriter) writeHeaderAndBody(bs []byte) (n int, err error) {
	if r.err != nil {
		return 0, r.err
	}

	if r.sentHeader {
		return 0, nil
	}

	if err = r.ensureHeader(); err != nil {
		return 0, err
	}

	r.sentHeader = true
	r.headerBuffer.WriteString("\r\n")
	if len(bs) > 0 {
		r.headerBuffer.Write(bs)
	}
	if r.c == nil {
		r.err = fmt.Errorf("turbo: response writer has no connection")
		return 0, r.err
	}

	n, err = r.c.Write(r.headerBuffer.Bytes())
	return r.recordWriteResult(n, r.headerBuffer.Len(), err)
}

func (r *responseWriter) writeBody(bs []byte) (n int, err error) {
	if err = r.validateStatusCodeForWriting(); err != nil {
		return 0, err
	}

	if err = r.validateContentLength(bs); err != nil {
		return 0, err
	}

	if r.head {
		r.written += int64(len(bs))
		return len(bs), nil
	}

	n, err = r.c.Write(bs)
	r.written += int64(n)
	return r.recordWriteResult(n, len(bs), err)
}

func (r *responseWriter) recordWriteResult(n, expected int, err error) (int, error) {
	if n < expected && err == nil {
		err = io.ErrShortWrite
	}

	r.err = err
	return n, err
}

func (r *responseWriter) validateStatusCodeForWriting() (err error) {
	switch {
	case r.statusCode < 200:
		return http.ErrBodyNotAllowed
	case r.statusCode == http.StatusNoContent:
		return http.ErrBodyNotAllowed
	case r.statusCode == http.StatusNotModified:
		return http.ErrBodyNotAllowed
	default:
		return nil
	}
}

func (r *responseWriter) validateContentLength(bs []byte) (err error) {
	switch {
	case r.contentLength < 0:
		return nil
	case int64(len(bs)) > r.contentLength-r.written:
		return http.ErrContentLength
	default:
		return nil
	}
}
