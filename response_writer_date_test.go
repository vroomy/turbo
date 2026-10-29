package turbo

import (
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestResponseWriterDateCache(t *testing.T) {
	// Start with an uninitialized writer at Unix second zero, then cross both
	// forward and backward boundaries. Inputs use a non-UTC location.
	zone := time.FixedZone("test", -7*60*60)
	var r responseWriter
	for _, tt := range []struct {
		name string
		sec  int64
		nsec int64
	}{
		{"epoch", 0, 0},
		{"same second", 0, 999999999},
		{"rollover", 1, 0},
		{"backward", 0, 500000000},
		{"before epoch", -1, 999999999},
		{"forward jump", 1800000000, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(tt.sec, tt.nsec).In(zone)
			previous := r.dateValue
			previousSecond := r.dateSecond
			if got, want := r.dateHeader(now), now.UTC().Format(http.TimeFormat); got != want {
				t.Fatalf("Date = %q, want %q", got, want)
			}
			if previous != "" && previous != time.Unix(previousSecond, 0).UTC().Format(http.TimeFormat) {
				t.Fatal("refresh mutated the previous Date string")
			}
			if allocs := testing.AllocsPerRun(100, func() { r.dateHeader(now) }); allocs != 0 {
				t.Fatalf("cache hit allocated %g objects, want zero", allocs)
			}
		})
	}
}

func TestResponseWriterDateCacheReuse(t *testing.T) {
	r := makeResponseWriter()
	now := time.Unix(1800000000, 123)
	want := r.dateHeader(now)
	r.populateHeader("Date", want)
	retained := r.Header().Clone()
	r.reset()
	if r.dateSecond != now.Unix() || r.dateValue != want {
		t.Fatal("reset discarded the Date cache")
	}
	if allocs := testing.AllocsPerRun(100, func() { r.dateHeader(now) }); allocs != 0 {
		t.Fatalf("reused cache allocated %g objects, want zero", allocs)
	}
	r.populateHeader("Date", r.dateHeader(now.Add(time.Second)))
	if retained.Get("Date") != want {
		t.Fatal("refresh changed the previous response's cloned header")
	}
}

func TestResponseWriterDateOverrides(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values []string
	}{
		{"custom", []string{"Mon, 02 Jan 2006 15:04:05 GMT"}},
		{"nil suppression", nil},
		{"empty suppression", []string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := makeResponseWriter()
			for response := range 2 {
				c := new(responseConn)
				r.c = c
				if response == 0 {
					r.Header()["Date"] = tt.values
				}
				r.WriteHeader(http.StatusOK)
				// Date follows the same committed-header snapshot rules as
				// other fields, even if the handler changes it afterwards.
				r.Header().Set("Date", "changed after commit")
				if _, err := r.Write([]byte("body")); err != nil {
					t.Fatal(err)
				}
				res := readResponse(t, &c.data, http.MethodGet)
				assertResponseBody(t, res, "body")
				if response == 0 {
					if got := res.Header.Values("Date"); len(got) != len(tt.values) || (len(got) > 0 && !reflect.DeepEqual(got, tt.values)) {
						t.Fatalf("Date = %q, want %q", got, tt.values)
					}
				} else if got := res.Header.Get("Date"); got != r.dateValue {
					t.Fatalf("reused Date = %q, want cached default %q", got, r.dateValue)
				}
				r.reset()
				r.populateHeaders()
			}
		})
	}
}
