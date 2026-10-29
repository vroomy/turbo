package turbo

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/vroomy/turbo/request"
)

// IAB OpenRTB 2.6 examples, adapted into a matched banner request/response.
// Attribution and modifications: internal/benchdata/openrtb/README.md.
//
//go:embed internal/benchdata/openrtb/request.json
var openRTBRequest []byte

//go:embed internal/benchdata/openrtb/response.json
var openRTBResponse []byte

func openRTBWorkload() http11Workload {
	w := http11Workload{
		name: "OpenRTB", method: http.MethodPost,
		requestBody: openRTBRequest, responseBody: openRTBResponse,
	}
	header := fmt.Sprintf("POST /openrtb2/auction HTTP/1.1\r\nHost: benchmark.local\r\nConnection: close\r\nContent-Type: application/json\r\nAccept: application/json\r\nx-openrtb-version: 2.6\r\nContent-Length: %d\r\n\r\n", len(w.requestBody))
	w.requestWire = append([]byte(header), w.requestBody...)
	return w
}

func openRTBHandler(w http11Workload) http.HandlerFunc {
	length := strconv.Itoa(len(w.responseBody))
	return func(out http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/openrtb2/auction" ||
			req.Header.Get("Content-Type") != "application/json" || req.Header.Get("X-Openrtb-Version") != "2.6" {
			out.WriteHeader(http.StatusBadRequest)
			return
		}
		n, err := io.Copy(io.Discard, req.Body)
		if err != nil || n != int64(len(w.requestBody)) {
			out.WriteHeader(http.StatusBadRequest)
			return
		}
		out.Header().Set("Content-Type", "application/json")
		out.Header().Set("Content-Length", length)
		out.Header().Set("X-Openrtb-Version", "2.6")
		_, _ = out.Write(w.responseBody)
	}
}

// B.Loop runs one continuous timed window instead of replaying a calibrated
// benchmark body. Use separate processes for Turbo/Stdlib so GC state cannot
// carry over between the two minute-long samples. Metrics are process-wide,
// including the common client, connections and watchdog; this is not a
// server-only or JSON-codec benchmark.
func BenchmarkOpenRTBHTTP11(b *testing.B) {
	for _, transport := range []string{"Pipe", "TCP"} {
		b.Run(transport, func(b *testing.B) {
			for _, implementation := range []string{"Turbo", "Stdlib"} {
				b.Run(implementation, func(b *testing.B) {
					w := openRTBWorkload()
					benchmarkHTTP11Sustained(b, transport, implementation, w, openRTBHandler(w))
				})
			}
		})
	}
}

// Use the same request and handler as the sustained transport benchmark, while
// excluding client response parsing, sockets, Pipe and watchdog allocations.
// This measures HTTP processing, not JSON decoding or auction logic.
func BenchmarkOpenRTBTurboProcess(b *testing.B) {
	w := openRTBWorkload()
	conn := &http11ProcessConn{}
	server := Turbo{h: openRTBHandler(w)}
	writer := makeResponseWriter()
	var parser request.Parser
	b.ReportAllocs()
	b.SetBytes(int64(len(w.requestBody) + len(w.responseBody)))
	for b.Loop() {
		conn.request.Reset(w.requestWire)
		if err := server.process(conn, &writer, &parser); err != nil {
			b.Fatal(err)
		}
	}
}

func TestOpenRTBContract(t *testing.T) {
	w := openRTBWorkload()
	var request struct {
		ID  string
		Imp []struct {
			ID       string
			BidFloor float64
		}
		Cur []string
	}
	var response struct {
		ID, Cur string
		SeatBid []struct {
			Bid []struct {
				ImpID string
				Price float64
			}
		}
	}
	if err := json.Unmarshal(w.requestBody, &request); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(w.responseBody, &response); err != nil {
		t.Fatal(err)
	}
	if request.ID == "" || request.ID != response.ID || len(request.Imp) != 1 || len(response.SeatBid) != 1 || len(response.SeatBid[0].Bid) != 1 {
		t.Fatal("fixtures must describe one matched auction and bid")
	}
	bid := response.SeatBid[0].Bid[0]
	if bid.ImpID != request.Imp[0].ID || bid.Price < request.Imp[0].BidFloor || len(request.Cur) != 1 || response.Cur != request.Cur[0] {
		t.Fatal("fixture impression, floor or currency mismatch")
	}
	for _, transport := range []string{"Pipe", "TCP"} {
		for _, implementation := range []string{"Turbo", "Stdlib"} {
			t.Run(transport+"/"+implementation, func(t *testing.T) {
				base := openRTBHandler(w)
				handler := func(out http.ResponseWriter, req *http.Request) {
					body, err := io.ReadAll(req.Body)
					if err != nil || !bytes.Equal(body, w.requestBody) {
						t.Error("request body changed in transit")
					}
					req.Body = io.NopCloser(bytes.NewReader(body))
					base(out, req)
				}
				dial := startHTTP11BenchmarkServer(t, transport, implementation, handler)
				for range 2 {
					conn, err := dial()
					if err != nil {
						t.Fatal(err)
					}
					defer conn.Close()
					if err := conn.SetDeadline(time.Now().Add(http11BenchmarkTimeout)); err != nil {
						t.Fatal(err)
					}
					if _, err := conn.Write(w.request()); err != nil {
						t.Fatal(err)
					}
					reader := bufio.NewReader(conn)
					res, err := http.ReadResponse(reader, &http.Request{Method: w.method})
					if err != nil {
						t.Fatal(err)
					}
					if res.StatusCode != http.StatusOK || res.Proto != "HTTP/1.1" || !res.Close || res.ContentLength != int64(len(w.responseBody)) || res.Header.Get("Content-Type") != "application/json" || res.Header.Get("X-Openrtb-Version") != "2.6" {
						t.Fatalf("unexpected response: %+v", res)
					}
					body, err := io.ReadAll(res.Body)
					if err != nil {
						t.Fatal(err)
					}
					if err := res.Body.Close(); err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(body, w.responseBody) {
						t.Fatal("response body changed in transit")
					}
					if _, err := reader.ReadByte(); err != io.EOF {
						t.Fatalf("expected EOF, got %v", err)
					}
					if err := conn.Close(); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
