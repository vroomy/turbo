// Package storagestudy provides deterministic fixtures for storage experiments.
// It is used only by tests and benchmarks.
package storagestudy

import (
	"fmt"
	"io"
	"strings"
)

// Reader ends the first read at Cut, independently of scheduling or transport
// buffering. Subsequent reads return the remaining request normally.
type Reader struct {
	Wire        string
	Cut, Offset int
}

func (r *Reader) Read(p []byte) (int, error) {
	if r.Offset == len(r.Wire) {
		return 0, io.EOF
	}
	end := len(r.Wire)
	if r.Offset < r.Cut {
		end = r.Cut
	}
	n := copy(p, r.Wire[r.Offset:end])
	r.Offset += n
	return n, nil
}

type Case struct {
	Name  string
	Wires []string
	Cut   int
}

func Fragments(method string) []Case {
	wire := method + " /bench HTTP/1.1\r\nHost: benchmark.local\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"
	return []Case{
		{"Complete", []string{wire}, 0},
		{"Early", []string{wire}, 2},
		{"Middle", []string{wire}, strings.Index(wire, "Connection") + 5},
		{"Final", []string{wire}, len(wire) - 1},
		// Valid duplicate lengths force stdlib fallback after ownHeader and
		// partial fast-path parsing, without changing the logical request.
		{"LateFallback", []string{strings.TrimSuffix(wire, "\r\n") + "Content-Length: 0\r\n\r\n"}, 0},
	}
}

// Sized makes target+fields exactly size bytes, excluding the final blank line.
func Sized(size int) string {
	const target = "/bench"
	const fields = "Host: benchmark.local\r\nConnection: close\r\nX-Pad: "
	return "GET " + target + " HTTP/1.1\r\n" + fields + strings.Repeat("x", size-len(target)-len(fields)-2) + "\r\n\r\n"
}

func Fields(count int) string {
	var b strings.Builder
	b.WriteString("GET /bench HTTP/1.1\r\nHost: benchmark.local\r\n")
	for i := range count {
		fmt.Fprintf(&b, "X-%03d: v\r\n", i)
	}
	b.WriteString("\r\n")
	return b.String()
}

func Sizes() []Case {
	var cases []Case
	for _, n := range []int{68, 128, 255, 256, 257, 512, 513, 704, 2048} {
		cases = append(cases, Case{fmt.Sprintf("Bytes%d", n), []string{Sized(n)}, 0})
	}
	return append(cases,
		Case{"AlternatingBytes", []string{Sized(68), Sized(2048)}, 0},
		Case{"AlternatingFields", []string{Fields(2), Fields(129)}, 0})
}
