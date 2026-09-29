package repeater

import (
	"context"
	"encoding/hex"
	"fmt"

	meshcore "github.com/meshcore-go/meshcore-go"
)

type PeerPathInfo struct {
	OutPath        string `json:"outPath"`
	Hops           int    `json:"hops"`
	HasPath        bool   `json:"hasPath"`
	DirectNeighbor bool   `json:"directNeighbor"`
	// OutPathHashSize is the route's own size; BytesPerHop is the contact's setting, which a flood or 0-hop send carries.
	OutPathHashSize int `json:"outPathHashSize"`
	BytesPerHop     int `json:"bytesPerHop"`
}

func (rm *Client) GetPeerPath(pubkeyHex string) (*PeerPathInfo, error) {
	pubkeyBytes, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid pubkey hex: %w", err)
	}

	peerIdentity, err := meshcore.NewIdentityFromBytes(pubkeyBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid pubkey: %w", err)
	}

	peer := rm.node.Peers().Lookup(peerIdentity.PublicKey())
	if peer == nil {
		return nil, fmt.Errorf("peer not found in peer table")
	}

	routeSize := int(max(peer.OutPathHashSize, 1))
	info := &PeerPathInfo{OutPathHashSize: routeSize, BytesPerHop: int(rm.bytesPerHop(pubkeyBytes))}
	if peer.OutPath != nil && len(peer.OutPath) == 0 {
		info.DirectNeighbor = true
		info.HasPath = true
		info.Hops = 0
	} else if len(peer.OutPath) > 0 {
		info.HasPath = true
		info.Hops = len(peer.OutPath) / routeSize
		info.OutPath = hex.EncodeToString(peer.OutPath)
	}
	return info, nil
}

func (rm *Client) ResetPeerPath(pubkeyHex string) error {
	pubkeyBytes, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return fmt.Errorf("invalid pubkey hex: %w", err)
	}

	peerIdentity, err := meshcore.NewIdentityFromBytes(pubkeyBytes)
	if err != nil {
		return fmt.Errorf("invalid pubkey: %w", err)
	}

	if !rm.node.Peers().ResetOutPath(peerIdentity.PublicKey()) {
		return fmt.Errorf("peer not found in peer table")
	}
	if err := rm.saveOutPath(pubkeyBytes, nil, 0); err != nil {
		return err
	}
	rm.log.Debug("peer path reset", "peer", pubkeyHex[:12])
	return nil
}

// SetPeerPath sets an operator's route, nil to flood and empty for a direct neighbour, with the bytes per hop everything sent uses.
func (rm *Client) SetPeerPath(pubkeyHex string, path []byte, bytesPerHop uint8) error {
	pubkeyBytes, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return fmt.Errorf("invalid pubkey hex: %w", err)
	}
	peerIdentity, err := meshcore.NewIdentityFromBytes(pubkeyBytes)
	if err != nil {
		return fmt.Errorf("invalid pubkey: %w", err)
	}
	key := peerIdentity.PublicKey()
	if rm.node.Peers().Lookup(key) == nil {
		return fmt.Errorf("peer not found in peer table")
	}
	// The row first and the table only once it is saved, so a failed save changes nothing live.
	var saveErr error
	rm.store.WriteSync(func() {
		if saveErr = rm.store.Contacts.SetRoute(context.Background(), rm.companionID, pubkeyBytes, path, bytesPerHop); saveErr == nil {
			rm.node.Peers().SetOutPath(key, path, bytesPerHop)
		}
	})
	if saveErr != nil {
		return saveErr
	}
	rm.log.Debug("peer path set", "peer", pubkeyHex[:12], "path", hex.EncodeToString(path), "flood", path == nil, "bytesPerHop", bytesPerHop)
	return nil
}

// saveOutPath is persistOutPath for an operator's change, written before the request returns so it cannot be dropped.
func (rm *Client) saveOutPath(pubkey []byte, path []byte, hashSize uint8) error {
	var err error
	rm.store.WriteSync(func() {
		err = rm.store.Contacts.UpdateOutPath(context.Background(), rm.companionID, pubkey, path, hashSize)
	})
	return err
}
