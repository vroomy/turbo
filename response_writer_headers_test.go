package turbo

import (
	"bytes"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

// Compare wire bytes to the stdlib serializer, including its exact-key
// exclusions. Direct map assignments need not use canonical or valid names.
func checkResponseHeaderSerialization(t testing.TB, headers http.Header, status int) {
	t.Helper()
	r := makeResponseWriter()
	r.headers = headers.Clone()
	r.statusCode = status
	if err := r.prepareHeader(); err != nil {
		t.Fatal(err)
	}
	want := makeResponseWriter()
	want.headers = headers.Clone()
	want.statusCode = status
	if !want.isInformational() {
		if err := want.readContentLength(); err != nil {
			t.Fatal(err)
		}
	}
	want.appendStatusLine()
	if err := headers.WriteSubset(want.headerBuffer, want.headerExclusions()); err != nil {
		t.Fatal(err)
	}
	if !want.isInformational() {
		want.appendFramingHeaders()
	}
	if !bytes.Equal(r.headerBuffer.Bytes(), want.headerBuffer.Bytes()) {
		t.Fatalf("status %d headers %q:\n got %q\nwant %q", status, headers, r.headerBuffer.Bytes(), want.headerBuffer.Bytes())
	}
	if !reflect.DeepEqual(r.headers, headers) {
		t.Fatal("serialization changed the handler's map or values")
	}
}

func TestResponseWriterHeaderSubset(t *testing.T) {
	for _, count := range []int{0, 2, 8, 63, 64, 65, 128, 1024} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			t.Parallel()
			headers := make(http.Header)
			for i := 0; i < count; i++ {
				headers[fmt.Sprintf("X-Field-%04d", i)] = []string{"first", "\t second\r\nline \t", ""}
			}
			for _, status := range []int{100, 101, 103, 200, 204, 304, 999} {
				checkResponseHeaderSerialization(t, headers, status)
			}
			headers["Connection"] = []string{"keep-alive"}
			headers["connection"] = []string{"raw lower case"}
			headers["Content-Length"] = []string{"+4"}
			headers["content-length"] = []string{"raw lower case"}
			headers["Content-Type"] = []string{"application/custom"}
			headers["content-type"] = []string{"raw lower case"}
			headers["Transfer-Encoding"] = []string{"chunked"}
			headers["Trailer"] = []string{"X-Trailer"}
			headers["Date"] = []string{"date one", "date two"}
			headers["date"] = []string{"raw lower case"}
			headers["X-Nil"] = nil
			headers["X-Empty"] = []string{}
			headers["Set-Cookie"] = []string{"a=1", "b=2"}
			headers[""] = []string{"invalid"}
			headers["Invalid Header"] = []string{"invalid"}
			headers["X-Unicode-\u00e9"] = []string{"invalid"}
			headers["X-Whitespace"] = []string{" \t\r\n ", "\v\fvalue\v\f", "\u00a0value\u00a0"}
			for _, date := range [][]string{headers["Date"], nil, {}} {
				headers["Date"] = date
				for _, status := range []int{100, 101, 103, 200, 204, 304, 999} {
					checkResponseHeaderSerialization(t, headers, status)
				}
			}
		})
	}
}

func TestResponseWriterHeaderBytes(t *testing.T) {
	for b := 0; b < 256; b++ {
		value := string([]byte{byte(b)})
		headers := http.Header{
			value:                 {"key byte alone"},
			"X-" + value + "-End": {"key byte inside"},
			"X-Value":             {value, value + "middle" + value, "start" + value + "end"},
		}
		checkResponseHeaderSerialization(t, headers, http.StatusOK)
	}
}

func TestResponseWriterPopulateHeadersLargeMap(t *testing.T) {
	r := makeResponseWriter()
	headers := r.Header()
	for _, count := range []int{0, 2, 8, 64, 1024, 0} {
		for i := 0; i < count; i++ {
			headers[fmt.Sprintf("X-%04d", i)] = []string{"first", "second"}
		}
		headers["connection"] = []string{"lowercase is a separate key"}
		headers["X-Nil"] = nil
		headers["X-Empty"] = []string{}
		// A handler can alias the default values to other fields and retain a
		// clone. Restoring defaults must neither overwrite nor retain them.
		oldValues := []string{"handler-owned"}
		headers["Connection"], headers["Date"] = oldValues, oldValues
		retained := headers.Clone()
		r.WriteHeader(http.StatusOK)
		r.reset()
		if r.headerBuffer.Len() != 0 || r.headerBuffer.Cap() > maxRetainedHeaderCapacity {
			t.Fatal("reset retained oversized serialized headers")
		}
		r.populateHeaders()
		if len(headers) != 2 || headers.Get("Connection") != "close" || headers.Get("Date") != r.dateValue {
			t.Fatalf("headers after %d fields = %v", count, headers)
		}
		if oldValues[0] != "handler-owned" || retained.Get("Connection") != "handler-owned" || retained.Get("Date") != "handler-owned" {
			t.Fatal("default restoration mutated retained handler values")
		}
		connection, date := headers["Connection"], headers["Date"]
		r.populateHeaders()
		if &connection[0] != &headers["Connection"][0] {
			t.Fatal("matching Connection slice was replaced")
		}
		if date[0] == r.dateValue && &date[0] != &headers["Date"][0] {
			t.Fatal("matching Date slice was replaced")
		}
		headers["X-Shared-Map"] = []string{"still shared"}
		if r.Header().Get("X-Shared-Map") != "still shared" {
			t.Fatal("header map was replaced")
		}
	}
}
