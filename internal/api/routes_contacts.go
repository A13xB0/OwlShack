package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	meshcore "github.com/meshcore-go/meshcore-go"

	"github.com/meshcore-go/OwlShack/internal/store"
)

type contactJSON struct {
	PeerPubKey      string                `json:"peerPubkey"`
	Name            string                `json:"name"`
	Type            string                `json:"type"`
	Lat             int32                 `json:"lat"`
	Lon             int32                 `json:"lon"`
	Feat1           uint16                `json:"feat1"`
	Feat2           uint16                `json:"feat2"`
	OutPath         *string               `json:"outPath"` // null = unknown (flood), "" = direct
	OutPathHashSize uint8                 `json:"outPathHashSize"`
	PathHashSize    uint8                 `json:"pathHashSize"` // bytes per hop for everything sent
	LastSeen        string                `json:"lastSeen,omitempty"`
	LastAdvertTS    uint32                `json:"lastAdvertTs"`
	AddedAt         string                `json:"addedAt"`
	Metadata        store.ContactMetadata `json:"metadata"`
}

// contactToJSON serializes a contact's own cached record, independent of discovered_peers.
func (s *Server) contactToJSON(c *store.Contact) contactJSON {
	lastSeen := ""
	if !c.LastSeen.IsZero() {
		lastSeen = c.LastSeen.UTC().Format(time.RFC3339)
	}
	return contactJSON{
		PeerPubKey:      hex.EncodeToString(c.PeerPubKey),
		Name:            c.Name,
		Type:            c.Type,
		Lat:             c.Lat,
		Lon:             c.Lon,
		Feat1:           c.Feat1,
		Feat2:           c.Feat2,
		OutPath:         outPathHex(c.OutPath),
		OutPathHashSize: c.OutPathHashSize,
		PathHashSize:    c.PathHashSize,
		LastSeen:        lastSeen,
		LastAdvertTS:    c.LastAdvertTS,
		AddedAt:         c.AddedAt.UTC().Format(time.RFC3339),
		Metadata:        c.Metadata,
	}
}

func outPathHex(p []byte) *string {
	if p == nil {
		return nil
	}
	h := hex.EncodeToString(p)
	return &h
}

// validPeerType mirrors meshcore-go's advert type-string mapping.
func validPeerType(t string) bool {
	switch t {
	case "CHAT", "REPEATER", "ROOM", "SENSOR":
		return true
	}
	return false
}

func (s *Server) handleListContacts(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	contacts, err := s.store.Contacts.List(r.Context(), cid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list contacts")
		return
	}

	out := make([]contactJSON, 0, len(contacts))
	for _, c := range contacts {
		out = append(out, s.contactToJSON(&c))
	}

	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetContact(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	pubkey, err := hex.DecodeString(r.PathValue("pubkey"))
	if err != nil || len(pubkey) == 0 {
		writeError(w, http.StatusBadRequest, "invalid pubkey hex")
		return
	}

	c, err := s.store.Contacts.Get(r.Context(), cid, pubkey)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "contact not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load contact")
		return
	}

	writeJSON(w, http.StatusOK, s.contactToJSON(c))
}

func (s *Server) handleAddContact(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	var body struct {
		PubKey string `json:"pubkey"`
		Name   string `json:"name"`
		Type   string `json:"type"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	pubkey, err := hex.DecodeString(body.PubKey)
	if err != nil || len(pubkey) != meshcore.PubKeySize {
		writeError(w, http.StatusBadRequest, "pubkey must be 64 hex chars")
		return
	}

	// Our own companion identities show up as discovered peers, so reject a self-contact.
	if comps, lerr := s.store.Companions.List(r.Context()); lerr == nil {
		selfHex := hex.EncodeToString(pubkey)
		for _, c := range comps {
			if strings.EqualFold(c.PubKey, selfHex) {
				writeError(w, http.StatusBadRequest, "cannot add your own node as a contact")
				return
			}
		}
	}

	// Optional display metadata (manual entry or a shared-contact embed).
	contactName := strings.TrimSpace(body.Name)
	contactType := strings.ToUpper(strings.TrimSpace(body.Type))
	if contactType != "" && !validPeerType(contactType) {
		writeError(w, http.StatusBadRequest, "invalid contact type")
		return
	}

	// A known peer is only relabelled when nameless, so a crafted shared-contact can't rename a trusted peer.
	existing, _ := s.store.Peers.GetByPubKey(r.Context(), pubkey)

	// The contact owns its identity now; backfill from a known peer when blank.
	storeName, storeType := contactName, contactType
	if storeName == "" && existing != nil {
		storeName = existing.Name
		if storeType == "" {
			storeType = existing.Type
		}
	}

	var seedPeer *store.Peer
	if existing == nil {
		seedPeer = &store.Peer{
			PubKey:   pubkey,
			Name:     contactName,
			Type:     contactType,
			LastSeen: time.Now(),
		}
	} else if existing.Name == "" && (contactName != "" || contactType != "") {
		existing.Name = contactName
		if contactType != "" {
			existing.Type = contactType
		}
		seedPeer = existing
	}

	var addErr error
	s.store.WriteSync(func() {
		// Seed the peer so it appears in the Peers list.
		if seedPeer != nil {
			_ = s.store.Peers.Upsert(r.Context(), seedPeer)
		}
		if addErr = s.store.Contacts.Add(r.Context(), cid, pubkey, storeName, storeType); addErr != nil {
			return
		}
		// Backfill from the heard peer so location/feat don't wait for the next advert; a send-path is only ever learned (BaseChatMesh.cpp:111).
		if existing != nil {
			hasLoc := existing.HasLocation()
			_ = s.store.Contacts.RefreshFromAdvert(
				r.Context(), pubkey, existing.Name, existing.Type,
				existing.Lat, existing.Lon, existing.Feat1, existing.Feat2,
				existing.LastSeen, existing.LastAdvertTS, hasLoc,
			)
		}
	})
	if addErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to add contact")
		return
	}

	// Without this, a contact added by pubkey alone fails every radio op until a restart re-hydrates it.
	if add := s.peerAdder(); add != nil {
		add(pubkey, storeName, storeType)
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteContact(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	pubkeyHex := r.PathValue("pubkey")

	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	pubkey, err := hex.DecodeString(pubkeyHex)
	if err != nil || len(pubkey) == 0 {
		writeError(w, http.StatusBadRequest, "invalid pubkey hex")
		return
	}

	var delErr error
	s.store.WriteSync(func() {
		delErr = s.store.Contacts.Delete(r.Context(), cid, pubkey)
	})
	if delErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete contact")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// maxLoginPassword is the firmware's char password[16], so a longer one can never log in.
const maxLoginPassword = 15

// monitorProbes are the bundles the poller runs; naming none runs them all.
var monitorProbes = []string{"status", "telemetry", "neighbors"}

// checkContactPatch refuses an unknown or null field, since either would change nothing and still answer 204, and holds each named value to what the form offers.
func checkContactPatch(patch json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(patch, &fields); err != nil || fields == nil {
		return errors.New("the body must be a JSON object of the fields to change")
	}
	for _, k := range slices.Sorted(maps.Keys(fields)) {
		if bytes.Equal(bytes.TrimSpace(fields[k]), []byte("null")) {
			return fmt.Errorf("%s may not be null; send its empty value to clear it", k)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(patch))
	dec.DisallowUnknownFields()
	var m store.ContactMetadata
	if err := dec.Decode(&m); err != nil {
		if te := (*json.UnmarshalTypeError)(nil); errors.As(err, &te) {
			return fmt.Errorf("%s cannot be %s", te.Field, te.Value)
		}
		return errors.New(strings.TrimPrefix(err.Error(), "json: "))
	}

	for _, pw := range []struct{ name, v string }{{"repeaterPassword", m.RepeaterPassword}, {"roomPassword", m.RoomPassword}} {
		if len(pw.v) > maxLoginPassword {
			return fmt.Errorf("%s is %d bytes, and a node keeps at most %d", pw.name, len(pw.v), maxLoginPassword)
		}
		if strings.ContainsFunc(pw.v, unicode.IsControl) {
			return fmt.Errorf("%s has a control character in it", pw.name)
		}
	}
	if v := m.MonitorIntervalSecs; v != 0 && (v < 900 || v > 86400) {
		return fmt.Errorf("monitorIntervalSecs %d is outside 900 to 86400, or 0 for the default", v)
	}
	if v := m.MonitorRetrySecs; v != 0 && (v < 60 || v > 1800) {
		return fmt.Errorf("monitorRetrySecs %d is outside 60 to 1800, or 0 for the default", v)
	}
	if v := m.MonitorMaxRetries; v < -1 || v > 10 {
		return fmt.Errorf("monitorMaxRetries %d is outside -1 (none) to 10", v)
	}
	for i, p := range m.MonitorProbes {
		if !slices.Contains(monitorProbes, p) {
			return fmt.Errorf("monitorProbes has %q, which is not one of %s", p, strings.Join(monitorProbes, ", "))
		}
		if slices.Contains(m.MonitorProbes[:i], p) {
			return fmt.Errorf("monitorProbes names %q twice", p)
		}
	}
	// The three TELEM_PERM_* classes: base, location and environment.
	if m.TelemPerms&^0x07 != 0 {
		return fmt.Errorf("telemPerms %#x has bits beyond the three classes", m.TelemPerms)
	}
	return nil
}

func (s *Server) handleUpdateContactMetadata(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	pubkeyHex := r.PathValue("pubkey")

	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	pubkey, err := hex.DecodeString(pubkeyHex)
	if err != nil || len(pubkey) == 0 {
		writeError(w, http.StatusBadRequest, "invalid pubkey hex")
		return
	}

	// Decoded onto what is stored, so a partial caller changes only the fields it names.
	var patch json.RawMessage
	if err := readJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := checkContactPatch(patch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var updateErr error
	s.store.WriteSync(func() {
		c, err := s.store.Contacts.Get(r.Context(), cid, pubkey)
		if err != nil {
			updateErr = err
			return
		}
		meta := c.Metadata
		if updateErr = json.Unmarshal(patch, &meta); updateErr == nil {
			updateErr = s.store.Contacts.UpdateMetadata(r.Context(), cid, pubkey, meta)
		}
	})
	if errors.Is(updateErr, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "contact not found")
		return
	}
	if updateErr != nil {
		s.serverError(w, "failed to update contact metadata", updateErr)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleSetContactLocation takes lat/lon in microdegrees; a later advert with a position overwrites it.
func (s *Server) handleSetContactLocation(w http.ResponseWriter, r *http.Request) {
	cid, ok := s.companionID(r.Context(), w, r.PathValue("name"))
	if !ok {
		return
	}
	pubkey, err := hex.DecodeString(r.PathValue("pubkey"))
	if err != nil || len(pubkey) == 0 {
		writeError(w, http.StatusBadRequest, "invalid pubkey hex")
		return
	}
	var body struct {
		Lat int32 `json:"lat"`
		Lon int32 `json:"lon"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var uerr error
	s.store.WriteSync(func() {
		uerr = s.store.Contacts.SetLocation(r.Context(), cid, pubkey, body.Lat, body.Lon)
	})
	if uerr != nil {
		writeError(w, http.StatusInternalServerError, "failed to set contact location")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// companionID maps the name in the URL to the surrogate id, writing a 404 when there is no such companion.
func (s *Server) companionID(ctx context.Context, w http.ResponseWriter, name string) (int64, bool) {
	id, err := s.store.Companions.IDByName(ctx, name)
	if err != nil {
		writeError(w, http.StatusNotFound, "companion not found")
		return 0, false
	}
	return id, true
}
