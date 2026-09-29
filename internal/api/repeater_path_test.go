package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/meshcore-go/OwlShack/internal/store"
)

type pathBackend struct {
	Backend
	set []setCall
	err error
}

type setCall struct {
	path []byte
	hs   uint8
}

func (b *pathBackend) Repeater(string) (*RepeaterOps, bool) {
	return &RepeaterOps{PathSet: func(_ string, path []byte, hs uint8) error {
		b.set = append(b.set, setCall{path, hs})
		return b.err
	}}, true
}

func putPath(t *testing.T, b *pathBackend, url, body string) *httptest.ResponseRecorder {
	t.Helper()
	s := &Server{mux: http.NewServeMux()}
	s.routes()
	s.SetBackend(b)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, url, strings.NewReader(body)))
	return rec
}

// The length byte packs the hop count and the hash size, so a path it cannot describe went on air with bytes the far end reads as payload.
func TestRepeaterPathSet_OnlyWhatTheLengthByteCanDescribe(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body    string
		want          int
		flood, direct bool
		hs            uint8
	}{
		{"flood at 2", `{"route":"flood","pathHashSize":2}`, http.StatusOK, true, false, 2},
		{"direct at 3", `{"route":"direct","pathHashSize":3}`, http.StatusOK, false, true, 3},
		{"one 2-byte hop", `{"route":"path","path":"aabb","pathHashSize":2}`, http.StatusOK, false, false, 2},
		{"63 1-byte hops", `{"route":"path","path":"` + strings.Repeat("ab", 63) + `","pathHashSize":1}`, http.StatusOK, false, false, 1},
		{"32 2-byte hops, 64 bytes", `{"route":"path","path":"` + strings.Repeat("ab", 64) + `","pathHashSize":2}`, http.StatusOK, false, false, 2},
		{"a path with no hops", `{"route":"path","path":"","pathHashSize":1}`, http.StatusBadRequest, false, false, 0},
		{"not whole hops", `{"route":"path","path":"aabbcc","pathHashSize":2}`, http.StatusBadRequest, false, false, 0},
		{"size 0", `{"route":"flood","pathHashSize":0}`, http.StatusBadRequest, false, false, 0},
		{"size 4", `{"route":"direct","pathHashSize":4}`, http.StatusBadRequest, false, false, 0},
		{"not hex", `{"route":"path","path":"zz","pathHashSize":1}`, http.StatusBadRequest, false, false, 0},
		{"64 1-byte hops", `{"route":"path","path":"` + strings.Repeat("ab", 64) + `","pathHashSize":1}`, http.StatusBadRequest, false, false, 0},
		{"66 bytes at 3", `{"route":"path","path":"` + strings.Repeat("ab", 66) + `","pathHashSize":3}`, http.StatusBadRequest, false, false, 0},
		{"no route mode", `{"path":"aabb","pathHashSize":1}`, http.StatusBadRequest, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &pathBackend{}
			rec := putPath(t, b, "/api/companions/home/repeaters/ab/path", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("%d %s, want %d", rec.Code, rec.Body, tc.want)
			}
			if tc.want != http.StatusOK {
				if len(b.set) != 0 {
					t.Errorf("saved %+v despite the refusal", b.set)
				}
				return
			}
			if len(b.set) != 1 {
				t.Fatalf("saved %d times", len(b.set))
			}
			got := b.set[0]
			if (got.path == nil) != tc.flood || (got.path != nil && len(got.path) == 0) != tc.direct || got.hs != tc.hs {
				t.Errorf("saved path %x (nil %v) at %d", got.path, got.path == nil, got.hs)
			}
		})
	}
}

// The chat and contact pages set a path through the contact URL, and a node that is not a contact is told why rather than given a 500.
func TestContactPathSet_SameRulesAndANonContactIs404(t *testing.T) {
	t.Parallel()
	b := &pathBackend{}
	if rec := putPath(t, b, "/api/companions/home/contacts/ab/path", `{"route":"direct","pathHashSize":2}`); rec.Code != http.StatusOK || len(b.set) != 1 {
		t.Fatalf("contact PUT: %d %s, %d saves", rec.Code, rec.Body, len(b.set))
	}
	b = &pathBackend{err: store.ErrNotContact}
	if rec := putPath(t, b, "/api/companions/home/contacts/ab/path", `{"route":"flood","pathHashSize":1}`); rec.Code != http.StatusNotFound {
		t.Errorf("non-contact: %d %s, want 404", rec.Code, rec.Body)
	}
}
