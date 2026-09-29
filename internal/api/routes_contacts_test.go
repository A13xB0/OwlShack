package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/meshcore-go/OwlShack/internal/store"
)

// A PATCH names what it changes: replacing the blob let the repeater list's {"isRepeater":true} wipe a saved password and a telemetry grant.
func TestContactMetadataPatch_KeepsWhatItDoesNotName(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "contacts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := store.Companion{Name: "home"}
	pub := bytes.Repeat([]byte{0xAB}, 32)
	st.WriteSync(func() {
		if err := st.Companions.Create(ctx, &c); err != nil {
			t.Fatal(err)
		}
		if err := st.Contacts.Add(ctx, c.ID, pub, "rep", "REPEATER"); err != nil {
			t.Fatal(err)
		}
		err = st.Contacts.UpdateMetadata(ctx, c.ID, pub, store.ContactMetadata{RepeaterPassword: "pw", TelemPerms: 5, MonitorProbes: []string{"status"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	patch := func(key []byte, body string) int {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/companions/home/contacts/"+hex.EncodeToString(key), strings.NewReader(body)))
		return rec.Code
	}
	stored := func() store.ContactMetadata {
		got, err := st.Contacts.Get(ctx, c.ID, pub)
		if err != nil {
			t.Fatal(err)
		}
		return got.Metadata
	}

	if code := patch(pub, `{"isRepeater":true}`); code != http.StatusNoContent {
		t.Fatalf("PATCH = %d", code)
	}
	if got, want := stored(), (store.ContactMetadata{IsRepeater: true, RepeaterPassword: "pw", TelemPerms: 5, MonitorProbes: []string{"status"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("stored %+v, want %+v", got, want)
	}

	// Naming a field with its empty value is how a caller clears it.
	if code := patch(pub, `{"repeaterPassword":"","monitorProbes":[]}`); code != http.StatusNoContent {
		t.Fatalf("PATCH = %d", code)
	}
	if got, want := stored(), (store.ContactMetadata{IsRepeater: true, TelemPerms: 5}); !reflect.DeepEqual(got, want) {
		t.Fatalf("stored %+v, want %+v", got, want)
	}

	// Each of these would change nothing or store what nothing can use, so each is refused and the row left alone.
	for _, body := range []string{
		`["isRepeater"]`,
		`{"monitorProbe":["status"]}`,
		`{"repeaterPassword":null}`,
		`{"monitorProbes":null}`,
		`{"monitor":"yes"}`,
		`{"telemPerms":300}`,
		`{"telemPerms":8}`,
		`{"monitorIntervalSecs":60}`,
		`{"monitorIntervalSecs":-900}`,
		`{"monitorRetrySecs":5000}`,
		`{"monitorMaxRetries":11}`,
		`{"monitorMaxRetries":-2}`,
		`{"monitorProbes":["status","bogus"]}`,
		`{"monitorProbes":["status","status"]}`,
		`{"repeaterPassword":"0123456789abcdef"}`,
		`{"roomPassword":"a\u0000b"}`,
	} {
		if code := patch(pub, body); code != http.StatusBadRequest {
			t.Errorf("%s got %d, want 400", body, code)
		}
	}
	if got, want := stored(), (store.ContactMetadata{IsRepeater: true, TelemPerms: 5}); !reflect.DeepEqual(got, want) {
		t.Fatalf("a refused PATCH changed the row: stored %+v, want %+v", got, want)
	}
	// The edges of what the form offers still save, so the refusals above are about the values.
	for _, body := range []string{`{}`, `{"monitorIntervalSecs":900}`, `{"monitorMaxRetries":-1}`, `{"repeaterPassword":"123456789012345"}`} {
		if code := patch(pub, body); code != http.StatusNoContent {
			t.Errorf("%s got %d, want 204", body, code)
		}
	}
	if code := patch(bytes.Repeat([]byte{0xCD}, 32), `{"isRepeater":true}`); code != http.StatusNotFound {
		t.Errorf("a contact that does not exist got %d, want 404", code)
	}
}

// Absence carried both "unknown" and "direct", so the contact page labelled a direct neighbour Flood.
func TestContactJSON_OutPathSaysUnknownOrDirect(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "contacts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := store.Companion{Name: "home"}
	cases := map[byte]struct {
		path []byte
		want string
	}{
		1: {nil, `"outPath":null`},
		2: {[]byte{}, `"outPath":""`},
		3: {[]byte{0xe6, 0x07}, `"outPath":"e607"`},
	}
	st.WriteSync(func() {
		if err = st.Companions.Create(ctx, &c); err != nil {
			return
		}
		for b, tc := range cases {
			pub := bytes.Repeat([]byte{b}, 32)
			if err = st.Contacts.Add(ctx, c.ID, pub, "rep", "REPEATER"); err == nil {
				err = st.Contacts.UpdateOutPath(ctx, c.ID, pub, tc.path, 1)
			}
			if err != nil {
				return
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for b, tc := range cases {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/companions/home/contacts/"+hex.EncodeToString(bytes.Repeat([]byte{b}, 32)), nil))
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("path %x: %d %s, want %s", tc.path, rec.Code, rec.Body, tc.want)
		}
	}
}

// A heard advert's path is how it reached us, not a route anyone learned; the firmware starts a new contact at unknown (BaseChatMesh.cpp:111).
func TestAddContact_LearnsNoRouteFromTheAdvert(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "contacts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := store.Companion{Name: "home"}
	pub := bytes.Repeat([]byte{0xcd}, 32)
	st.WriteSync(func() {
		if err = st.Companions.Create(ctx, &c); err == nil {
			err = st.Peers.Upsert(ctx, &store.Peer{PubKey: pub, Name: "rep", Type: "REPEATER", OutPath: []byte{0xaa, 0xbb}, OutPathHashSize: 1})
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/companions/home/contacts", strings.NewReader(`{"pubkey":"`+hex.EncodeToString(pub)+`"}`)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	got, err := st.Contacts.Get(ctx, c.ID, pub)
	if err != nil || got == nil {
		t.Fatalf("contact not added: %v", err)
	}
	if got.OutPath != nil {
		t.Errorf("new contact routes down %x, the advert's path reversed; want nil (flood until a PATH is learned)", got.OutPath)
	}
}
