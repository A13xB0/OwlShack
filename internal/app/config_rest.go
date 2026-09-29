package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// Every per-resource write validates the assembled result before it touches the DB, and keeps ids across renames.

// configMutate is the shared write transaction, serialized on the store's writer goroutine.
func (b *backend) configMutate(ctx context.Context, apply func(*configRows), persist func(*store.Store) error) error {
	return writeConfigTx(ctx, b.db, b.reload, func(rows *configRows) error {
		apply(rows)
		if verr := assembleFromRows(rows).Validate(); verr != nil {
			return verr
		}
		return persist(b.db)
	})
}

// writeConfigTx runs load → validate → persist inside the single writer, and reloads only if work succeeded.
func writeConfigTx(ctx context.Context, db *store.Store, reload func() error, work func(*configRows) error) error {
	var resultErr error
	db.WriteSync(func() {
		rows, err := loadConfigRows(ctx, db)
		if err != nil {
			resultErr = err
			return
		}
		resultErr = work(rows)
	})
	if resultErr != nil {
		return resultErr
	}
	if reload != nil {
		return reload()
	}
	return nil
}

func filterOut[T any](s []T, drop func(T) bool) []T {
	out := make([]T, 0, len(s))
	for _, x := range s {
		if !drop(x) {
			out = append(out, x)
		}
	}
	return out
}

// or returns p when set, else the default d.
func or[T any](p, d *T) *T {
	if p != nil {
		return p
	}
	return d
}

func (b *backend) SaveSettings(ctx context.Context, in api.SettingsInput) error {
	def := config.DefaultConfig()
	var row store.Settings
	return b.configMutate(ctx,
		func(rows *configRows) {
			// Derived, never taken from the caller: the connection string is what Setup switches
			// on, so a stored backend that disagreed with it would be a lie the UI reads back.
			ct := "kiss"
			conn := or(in.Connection, def.Connection)
			if scheme, _, ok := config.ParseConnection(strDeref(conn)); ok && scheme != "serial" && scheme != "tcp" {
				ct = scheme
			}
			// SetupComplete changes only when explicitly provided, so a radio edit never re-opens the wizard.
			setup := rows.settings.SetupComplete
			if in.SetupComplete != nil {
				setup = *in.SetupComplete
			}
			prevKey := rows.settings.MapTileKey
			prevToken := rows.settings.ModemToken
			prevBoard := rows.settings.SPIBoard
			row = store.Settings{
				LogLevel:       in.LogLevel,
				ConnectionType: ct,
				Connection:     conn,
				BaudRate:       or(in.BaudRate, def.BaudRate),
				SPIBoard:       or(in.SPIBoard, prevBoard),
				Freq:           or(in.Freq, def.Freq),
				BW:             or(in.BW, def.Bw),
				SF:             or(in.SF, u8ToIntPtr(def.SF)),
				CR:             or(in.CR, u8ToIntPtr(def.CR)),
				TX:             or(in.TX, u8ToIntPtr(def.TX)),
				ListenAddr:     in.ListenAddr,
				MapProvider:    *or(in.MapProvider, &rows.settings.MapProvider),
				MapDarkStyle:   *or(in.MapDarkStyle, &rows.settings.MapDarkStyle),
				MapTileKey:     or(in.MapTileKey, prevKey),
				ModemToken:     or(in.ModemToken, prevToken),
				PathHashSize:   in.PathHashSize,
				DutyCyclePct:   in.DutyCycle,
				SetupComplete:  setup,
			}
			rows.settings = &row
		},
		func(st *store.Store) error { return st.Settings.Set(ctx, &row) },
	)
}

func (b *backend) SaveMqtt(ctx context.Context, in api.MqttInput) error {
	row := store.MqttSettings{
		Enabled:         in.Enabled,
		NodeCompanionID: in.NodeCompanionID,
		IataCode:        in.IataCode,
		StatusInterval:  in.StatusInterval,
		Owner:           in.Owner,
		Email:           in.Email,
	}
	return b.configMutate(ctx,
		func(rows *configRows) { rows.mqtt = &row },
		func(st *store.Store) error { return st.Mqtt.Set(ctx, &row) },
	)
}

func (b *backend) SaveBroker(ctx context.Context, in api.BrokerInput) (int64, error) {
	var row store.Broker
	err := b.configMutate(ctx,
		func(rows *configRows) {
			row = store.Broker{
				ID: in.ID, Name: in.Name, Enabled: in.Enabled, Dedup: in.Dedup,
				Transport: in.Transport, Host: in.Host, Port: in.Port,
				PacketTopic: in.PacketTopic, StatusTopic: in.StatusTopic,
				DisallowedPacketTypes: in.DisallowedPacketTypes, RetainStatus: in.RetainStatus,
				TLSEnabled: in.TLSEnabled, TLSInsecure: in.TLSInsecure, AuthType: in.AuthType,
				Username: in.Username, Path: in.Path, Audience: in.Audience,
			}
			switch {
			case in.Password != nil:
				row.Password = *in.Password
			case in.ID != 0:
				for _, x := range rows.brokers {
					if x.ID == in.ID {
						row.Password = x.Password
					}
				}
			}
			if in.ID == 0 {
				rows.brokers = append(rows.brokers, row)
			} else {
				for i := range rows.brokers {
					if rows.brokers[i].ID == in.ID {
						rows.brokers[i] = row
					}
				}
			}
		},
		func(st *store.Store) error {
			if row.ID == 0 {
				return st.Brokers.Create(ctx, &row)
			}
			return st.Brokers.Update(ctx, &row)
		},
	)
	return row.ID, err
}

func (b *backend) DeleteBroker(ctx context.Context, id int64) error {
	return b.configMutate(ctx,
		func(rows *configRows) {
			rows.brokers = filterOut(rows.brokers, func(x store.Broker) bool { return x.ID == id })
		},
		func(st *store.Store) error { return st.Brokers.Delete(ctx, id) },
	)
}

func (b *backend) SaveCompanion(ctx context.Context, in api.CompanionInput) (int64, error) {
	// Resolve the key up front so a generation failure surfaces before the tx.
	key := ""
	if in.PrivateKey != nil {
		key = *in.PrivateKey
	}
	if in.ID == 0 && key == "" {
		k, err := config.GenerateSeedHex()
		if err != nil {
			return 0, err
		}
		key = k
	}

	var row store.Companion
	var newPublic *store.CompanionChannel
	err := b.configMutate(ctx,
		func(rows *configRows) {
			row = store.Companion{
				ID: in.ID, Name: in.Name,
				Latitude: in.Latitude, Longitude: in.Longitude, AdvertInterval: in.AdvertInterval,
				PathHashSize: in.PathHashSize,
				DMPolicy:     in.DMPolicy, DMAllow: in.DMAllow,
			}
			row.PrivateKey = key
			// Telemetry modes have their own endpoint, so an edit from any other form must carry them through.
			for _, c := range rows.companions {
				if c.ID != in.ID || in.ID == 0 {
					continue
				}
				if row.PrivateKey == "" { // update without a key change → keep existing
					row.PrivateKey = c.PrivateKey
				}
				row.TelemBase, row.TelemLoc, row.TelemEnv = c.TelemBase, c.TelemLoc, c.TelemEnv
			}
			row.PubKey, _ = config.PubKeyHexFromSeed(row.PrivateKey)

			if in.ID == 0 {
				rows.companions = append(rows.companions, row)
				// Every companion is always joined to Public.
				newPublic = &store.CompanionChannel{Name: "Public"}
				rows.channels = append(rows.channels, *newPublic)
			} else {
				for i := range rows.companions {
					if rows.companions[i].ID == in.ID {
						rows.companions[i] = row
					}
				}
			}
		},
		func(st *store.Store) error {
			if row.ID != 0 {
				return st.Companions.Update(ctx, &row)
			}
			if err := st.Companions.Create(ctx, &row); err != nil {
				return err
			}
			newPublic.CompanionID = row.ID
			return st.Channels.Create(ctx, newPublic)
		},
	)
	return row.ID, err
}

// SetCompanionTelemetry is its own endpoint, or every other companion form would have to carry the modes.
func (b *backend) SetCompanionTelemetry(ctx context.Context, id int64, in api.CompanionTelemetryInput) error {
	modes := []string{in.Base, in.Location, in.Environment}
	for _, m := range modes {
		switch m {
		case config.TelemetryDeny, config.TelemetrySelected, config.TelemetryContacts:
		default:
			return fmt.Errorf("%q is not a telemetry mode", m)
		}
	}
	found := false
	var row store.Companion
	err := b.configMutate(ctx,
		func(rows *configRows) {
			for i := range rows.companions {
				if rows.companions[i].ID != id {
					continue
				}
				rows.companions[i].TelemBase = in.Base
				rows.companions[i].TelemLoc = in.Location
				rows.companions[i].TelemEnv = in.Environment
				row, found = rows.companions[i], true
			}
		},
		func(st *store.Store) error {
			if !found {
				return fmt.Errorf("no companion with id %d", id)
			}
			return st.Companions.Update(ctx, &row)
		},
	)
	return err
}

func (b *backend) DeleteCompanion(ctx context.Context, id int64) error {
	return b.configMutate(ctx,
		func(rows *configRows) {
			rows.companions = filterOut(rows.companions, func(c store.Companion) bool { return c.ID == id })
			rows.channels = filterOut(rows.channels, func(c store.CompanionChannel) bool { return c.CompanionID == id })
			rows.triggers = filterOut(rows.triggers, func(t store.Trigger) bool { return t.CompanionID == id })
			if rows.mqtt.NodeCompanionID != nil && *rows.mqtt.NodeCompanionID == id {
				rows.mqtt.NodeCompanionID = nil
			}
		},
		func(st *store.Store) error { return st.Companions.Delete(ctx, id) }, // cascade clears children
	)
}

func (b *backend) SaveChannel(ctx context.Context, in api.ChannelInput) (int64, error) {
	var row store.CompanionChannel
	err := b.configMutate(ctx,
		func(rows *configRows) {
			row = store.CompanionChannel{ID: in.ID, CompanionID: in.CompanionID, Name: in.Name}
			switch {
			case in.PrivateKey != nil:
				row.PrivateKey = *in.PrivateKey
			case in.ID != 0:
				for _, c := range rows.channels {
					if c.ID == in.ID {
						row.PrivateKey = c.PrivateKey
						if row.CompanionID == 0 {
							row.CompanionID = c.CompanionID
						}
					}
				}
			}
			if in.ID == 0 {
				rows.channels = append(rows.channels, row)
			} else {
				for i := range rows.channels {
					if rows.channels[i].ID == in.ID {
						rows.channels[i] = row
					}
				}
			}
		},
		func(st *store.Store) error {
			if row.ID == 0 {
				return st.Channels.Create(ctx, &row)
			}
			return st.Channels.Update(ctx, &row)
		},
	)
	return row.ID, err
}

func (b *backend) DeleteChannel(ctx context.Context, id int64) error {
	return b.configMutate(ctx,
		func(rows *configRows) {
			rows.channels = filterOut(rows.channels, func(c store.CompanionChannel) bool { return c.ID == id })
		},
		func(st *store.Store) error { return st.Channels.Delete(ctx, id) }, // cascade clears trigger links
	)
}

func (b *backend) SaveTrigger(ctx context.Context, in api.TriggerInput) (int64, error) {
	row := store.Trigger{
		ID: in.ID, CompanionID: in.CompanionID, Type: in.Type, Template: in.Template,
		CharLimitBehaviour: in.CharLimitBehaviour, MatchPatterns: in.Match, Contacts: in.Contacts,
		RetryTimeout: in.RetryTimeout, MaxRetries: in.MaxRetries, PathHashSize: in.PathHashSize,
		Schedule: in.Schedule, URL: in.URL, ChannelIDs: in.ChannelIDs,
		FailoverPattern: in.FailoverPattern, FailoverTimeout: in.FailoverTimeout,
		Regions: in.Regions,
	}
	loc, err := locationFromAPI(in.Location)
	if err != nil {
		return 0, api.Invalid(err)
	}
	row.Location = locationToStore(loc)
	err = b.configMutate(ctx,
		func(rows *configRows) {
			if in.ID == 0 {
				rows.triggers = append(rows.triggers, row)
			} else {
				for i := range rows.triggers {
					if rows.triggers[i].ID == in.ID {
						rows.triggers[i] = row
					}
				}
			}
		},
		func(st *store.Store) error {
			if row.ID == 0 {
				return st.Triggers.Create(ctx, &row)
			}
			return st.Triggers.Update(ctx, &row)
		},
	)
	return row.ID, err
}

// CreateRepeater seeds the "*" wildcard scope so the repeater relays unscoped flood, as the firmware implicitly does.
func (b *backend) CreateRepeater(ctx context.Context, in api.RepeaterCreateInput) error {
	if strings.TrimSpace(in.AdminPassword) == "" {
		return errors.New("admin password is required: a blank one grants admin to any node in range")
	}

	var row store.Repeater
	exists := false
	return b.configMutate(ctx,
		func(rows *configRows) {
			if rows.repeater != nil {
				exists = true
				return
			}
			row = store.Repeater{
				Name:          in.Name,
				AdminPassword: in.AdminPassword,
				Regions:       []store.RepeaterRegion{{Name: config.WildcardRegion}},
			}
			if in.PrivateKey != nil && *in.PrivateKey != "" {
				row.PrivateKey = *in.PrivateKey
			} else {
				row.PrivateKey, _ = config.GenerateSeedHex()
			}
			row.PubKey, _ = config.PubKeyHexFromSeed(row.PrivateKey)
			rows.repeater = &row
		},
		func(st *store.Store) error {
			if exists {
				return errors.New("repeater already configured")
			}
			return st.Repeater.Set(ctx, &row)
		},
	)
}

// mutateRepeater applies a partial change through one validated, reloaded write; it errors when no repeater exists.
func (b *backend) mutateRepeater(ctx context.Context, mutate func(*store.Repeater)) error {
	var row store.Repeater
	found := false
	return b.configMutate(ctx,
		func(rows *configRows) {
			if rows.repeater == nil {
				return
			}
			found = true
			row = *rows.repeater
			mutate(&row)
			rows.repeater = &row
		},
		func(st *store.Store) error {
			if !found {
				return errors.New("no repeater configured")
			}
			return st.Repeater.Set(ctx, &row)
		},
	)
}

// UpdateRepeaterNode edits the Node section; a supplied key value rotates the identity.
func (b *backend) UpdateRepeaterNode(ctx context.Context, in api.RepeaterNodeInput) error {
	return b.mutateRepeater(ctx, func(r *store.Repeater) {
		r.Name = in.Name
		r.Latitude = in.Latitude
		r.Longitude = in.Longitude
		if in.PrivateKey != nil && *in.PrivateKey != "" {
			r.PrivateKey = *in.PrivateKey
			r.PubKey, _ = config.PubKeyHexFromSeed(r.PrivateKey)
		}
	})
}

// UpdateRepeaterRelay edits the Relay-policy section.
func (b *backend) UpdateRepeaterRelay(ctx context.Context, in api.RepeaterRelayInput) error {
	return b.mutateRepeater(ctx, func(r *store.Repeater) {
		r.DisableFwd = in.DisableFwd
		r.FloodMax = in.FloodMax
		r.FloodMaxUnscoped = in.FloodMaxUnscoped
		r.FloodMaxAdvert = in.FloodMaxAdvert
		r.LoopDetect = in.LoopDetect
		r.PathHashSize = in.PathHashSize
		r.TxDelayFactor = in.TxDelayFactor
		r.DirectTxDelayFactor = in.DirectTxDelayFactor
		r.RxDelayBase = in.RxDelayBase
		r.MultiAcks = in.MultiAcks
		r.DefaultRegion = in.DefaultRegion
		r.AdvertInterval = in.AdvertInterval
		r.FloodAdvertInterval = in.FloodAdvertInterval
	})
}

// UpdateRepeaterAdmin edits the Owner & access section (nil password = keep).
func (b *backend) UpdateRepeaterAdmin(ctx context.Context, in api.RepeaterAdminInput) error {
	// Omitting the field keeps the stored value; sending "" would clear it, and a blank admin
	// password compares equal to the blank a login sends. Guest may still be cleared: blank guest
	// grants PERM_ACL_GUEST (0), which is what the firmware does.
	if in.AdminPassword != nil && strings.TrimSpace(*in.AdminPassword) == "" {
		return errors.New("admin password cannot be blank: omit the field to keep the current one")
	}
	return b.mutateRepeater(ctx, func(r *store.Repeater) {
		r.OwnerInfo = in.OwnerInfo
		r.AdminPassword = keepSecret(in.AdminPassword, r.AdminPassword)
		r.GuestPassword = keepSecret(in.GuestPassword, r.GuestPassword)
	})
}

// AddRepeaterRegion updates the deny-flood flag when the region exists, so a re-add is idempotent.
func (b *backend) AddRepeaterRegion(ctx context.Context, in api.RepeaterRegionInput) error {
	return b.mutateRepeater(ctx, func(r *store.Repeater) {
		for i := range r.Regions {
			if r.Regions[i].Name == in.Name {
				r.Regions[i].DenyFlood = in.DenyFlood
				return
			}
		}
		r.Regions = append(r.Regions, store.RepeaterRegion{Name: in.Name, DenyFlood: in.DenyFlood})
	})
}

// SetRepeaterRegionFlood toggles a region's deny-flood flag.
func (b *backend) SetRepeaterRegionFlood(ctx context.Context, name string, denyFlood bool) error {
	return b.mutateRepeater(ctx, func(r *store.Repeater) {
		for i := range r.Regions {
			if r.Regions[i].Name == name {
				r.Regions[i].DenyFlood = denyFlood
			}
		}
	})
}

// RemoveRepeaterRegion: removing "*" stops relaying unscoped flood (see regionsFromConfig).
func (b *backend) RemoveRepeaterRegion(ctx context.Context, name string) error {
	return b.mutateRepeater(ctx, func(r *store.Repeater) {
		r.Regions = slices.DeleteFunc(r.Regions, func(rg store.RepeaterRegion) bool { return rg.Name == name })
		if r.DefaultRegion == name {
			r.DefaultRegion = ""
		}
		if r.HomeRegion == name {
			r.HomeRegion = ""
		}
	})
}

func (b *backend) DeleteRepeater(ctx context.Context) error {
	return b.configMutate(ctx,
		func(rows *configRows) { rows.repeater = nil },
		func(st *store.Store) error {
			if err := st.RepeaterACL.Clear(ctx); err != nil { // drop admin-over-mesh clients
				return err
			}
			return st.Repeater.Clear(ctx)
		},
	)
}

// keepSecret implements the "omit to keep" contract for redacted secret fields.
func keepSecret(in *string, prev string) string {
	if in != nil {
		return *in
	}
	return prev
}

func (b *backend) DeleteTrigger(ctx context.Context, id int64) error {
	return b.configMutate(ctx,
		func(rows *configRows) {
			rows.triggers = filterOut(rows.triggers, func(t store.Trigger) bool { return t.ID == id })
		},
		func(st *store.Store) error { return st.Triggers.Delete(ctx, id) },
	)
}
