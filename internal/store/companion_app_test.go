package store

import (
	"bytes"
	"testing"
	"time"
)

// The operator's connection and the app's preferences are written by different hands, so neither write may clear the other.
func TestCompanionApp_ConnectionAndPrefsKeepEachOther(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	comp := &Companion{Name: "home"}
	if err := st.Companions.Create(ctx, comp); err != nil {
		t.Fatal(err)
	}

	if a, err := st.CompanionApps.Get(ctx, comp.ID); err != nil || a != DefaultCompanionApp(comp.ID) {
		t.Fatalf("unset companion = %+v, %v; want the defaults", a, err)
	}

	if err := st.CompanionApps.SetConnection(ctx, comp.ID, 5055, "0.0.0.0", true); err != nil {
		t.Fatal(err)
	}
	prefs := DefaultCompanionApp(comp.ID)
	prefs.ManualAdd, prefs.AutoAddConfig, prefs.AutoAddMaxHops, prefs.MultiAcks, prefs.AirtimeFactorMs = 1, 0x06, 3, 2, 2500
	if err := st.CompanionApps.SetPrefs(ctx, prefs); err != nil {
		t.Fatal(err)
	}
	if err := st.CompanionApps.SetConnection(ctx, comp.ID, 5056, "", false); err != nil {
		t.Fatal(err)
	}

	got, err := st.CompanionApps.Get(ctx, comp.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := prefs
	want.Port, want.Bind, want.AllowKeyExport = 5056, "", false
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if list, _ := st.CompanionApps.List(ctx); len(list) != 1 || list[0] != want {
		t.Errorf("List = %+v", list)
	}

	if err := st.Companions.Delete(ctx, comp.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.CompanionApps.List(ctx); len(list) != 0 {
		t.Errorf("deleting the companion left %+v", list)
	}
}

// Every write an app can see moves lastmod, which its GET_CONTACTS since-filter compares against.
func TestContacts_LastModAndFlags(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	comp := &Companion{Name: "home"}
	if err := st.Companions.Create(ctx, comp); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x42}, 32)
	before := uint32(time.Now().Unix())
	if err := st.Contacts.Add(ctx, comp.ID, key, "friend", "CHAT"); err != nil {
		t.Fatal(err)
	}
	if err := st.Contacts.SetFlags(ctx, comp.ID, key, 0x01); err != nil {
		t.Fatal(err)
	}
	ct, err := st.Contacts.Get(ctx, comp.ID, key)
	if err != nil {
		t.Fatal(err)
	}
	if ct.LastMod < before || ct.Flags != 0x01 {
		t.Errorf("lastmod %d flags %#x, want at least %d and 0x01", ct.LastMod, ct.Flags, before)
	}
}

func TestPeers_AdvertPayload(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	key := bytes.Repeat([]byte{0x43}, 32)
	if got, err := st.Peers.Advert(ctx, key); err != nil || got != nil {
		t.Fatalf("unknown peer = %x, %v; want nil", got, err)
	}
	if err := st.Peers.Upsert(ctx, &Peer{PubKey: key, Name: "rpt", LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	payload := []byte{1, 2, 3, 4}
	if err := st.Peers.SetAdvert(ctx, key, payload); err != nil {
		t.Fatal(err)
	}
	if err := st.Peers.Upsert(ctx, &Peer{PubKey: key, Name: "rpt", LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.Peers.Advert(ctx, key); err != nil || !bytes.Equal(got, payload) {
		t.Errorf("advert after a later upsert = %x, %v; want %x kept", got, err, payload)
	}
}
