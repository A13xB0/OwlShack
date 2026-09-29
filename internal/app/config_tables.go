package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// The full-document PUT carries no surrogate ids, so companions are upserted BY NAME to keep theirs stable; children are replaced wholesale.

// --- small pointer/type adapters between config (*uint8, string) and store (*int, *string) ---

func u8ToIntPtr(p *uint8) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

func intToU8Ptr(p *int) *uint8 {
	if p == nil {
		return nil
	}
	v := uint8(*p)
	return &v
}

func emptyToNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func ptrToStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefSlice(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

func sliceToPtr(s []string) *[]string {
	if len(s) == 0 {
		return nil
	}
	return &s
}

// configRows is the snapshot of every config table that the REST write path mutates, validates, then persists.
type configRows struct {
	settings   *store.Settings
	mqtt       *store.MqttSettings
	brokers    []store.Broker
	companions []store.Companion
	channels   []store.CompanionChannel // across all companions
	triggers   []store.Trigger          // across all companions, with ChannelIDs
	repeater   *store.Repeater          // the single repeater node, or nil
}

func loadConfigRows(ctx context.Context, st *store.Store) (*configRows, error) {
	s, err := st.Settings.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading settings: %w", err)
	}
	mq, err := st.Mqtt.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading mqtt: %w", err)
	}
	brokers, err := st.Brokers.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading brokers: %w", err)
	}
	comps, err := st.Companions.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading companions: %w", err)
	}
	chans, err := st.Channels.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading channels: %w", err)
	}
	trigs, err := st.Triggers.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading triggers: %w", err)
	}
	rep, err := st.Repeater.Get(ctx)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("reading repeater: %w", err)
		}
		rep = nil // no repeater configured
	}
	return &configRows{settings: s, mqtt: mq, brokers: brokers, companions: comps, channels: chans, triggers: trigs, repeater: rep}, nil
}

// assembleFromRows is pure, so the write path can validate a proposed edit before persisting it.
func assembleFromRows(rows *configRows) *config.Config {
	s := rows.settings
	cfg := &config.Config{
		LogLevel:       s.LogLevel,
		ConnectionType: emptyToNil(s.ConnectionType),
		Connection:     s.Connection,
		BaudRate:       s.BaudRate,
		SPIBoard:       s.SPIBoard,
		Freq:           s.Freq,
		Bw:             s.BW,
		SF:             intToU8Ptr(s.SF),
		CR:             intToU8Ptr(s.CR),
		TX:             intToU8Ptr(s.TX),
		ListenAddr:     s.ListenAddr,
		MapProvider:    &s.MapProvider,
		MapDarkStyle:   &s.MapDarkStyle,
		MapTileKey:     s.MapTileKey,
		ModemToken:     s.ModemToken,
		PathHashSize:   s.PathHashSize,
		DutyCycle:      s.DutyCyclePct,
		SetupComplete:  boolPtr(s.SetupComplete),
	}

	chansByComp := make(map[int64][]store.CompanionChannel)
	chanByID := make(map[int64]store.CompanionChannel, len(rows.channels))
	for _, ch := range rows.channels {
		chansByComp[ch.CompanionID] = append(chansByComp[ch.CompanionID], ch)
		chanByID[ch.ID] = ch
	}
	trigsByComp := make(map[int64][]store.Trigger)
	for _, t := range rows.triggers {
		trigsByComp[t.CompanionID] = append(trigsByComp[t.CompanionID], t)
	}

	idToName := make(map[int64]string, len(rows.companions))
	for _, c := range rows.companions {
		idToName[c.ID] = c.Name
		comp := config.CompanionConfig{
			ID:             c.ID,
			Name:           c.Name,
			PrivateKey:     c.PrivateKey,
			Latitude:       c.Latitude,
			Longitude:      c.Longitude,
			AdvertInterval: c.AdvertInterval,
			PathHashSize:   c.PathHashSize,
			DMPolicy:       emptyToNil(c.DMPolicy),
			DMAllow:        sliceToPtr(c.DMAllow),

			TelemetryBase:        emptyToNil(c.TelemBase),
			TelemetryLocation:    emptyToNil(c.TelemLoc),
			TelemetryEnvironment: emptyToNil(c.TelemEnv),
		}
		if chs := chansByComp[c.ID]; len(chs) > 0 {
			list := make(config.ChannelList, 0, len(chs))
			for _, ch := range chs {
				list = append(list, config.ChannelRef{Name: ch.Name, PrivateKey: ch.PrivateKey})
			}
			comp.Channels = &list
		}
		if ts := trigsByComp[c.ID]; len(ts) > 0 {
			tlist := make([]config.TriggerConfig, 0, len(ts))
			for _, t := range ts {
				tc := config.TriggerConfig{
					Type:               t.Type,
					Template:           t.Template,
					CharLimitBehaviour: t.CharLimitBehaviour,
					Match:              sliceToPtr(t.MatchPatterns),
					Contacts:           sliceToPtr(t.Contacts),
					RetryTimeout:       t.RetryTimeout,
					MaxRetries:         t.MaxRetries,
					PathHashSize:       intToU8Ptr(t.PathHashSize),
					Schedule:           ptrToStr(t.Schedule),
					URL:                t.URL,
					FailoverPattern:    t.FailoverPattern,
					FailoverTimeout:    t.FailoverTimeout,
					Location:           locationFromStore(t.Location),
					Regions:            t.Regions,
				}
				if len(t.ChannelIDs) > 0 {
					cl := make(config.ChannelList, 0, len(t.ChannelIDs))
					for _, cid := range t.ChannelIDs {
						if ch, ok := chanByID[cid]; ok {
							cl = append(cl, config.ChannelRef{Name: ch.Name, PrivateKey: ch.PrivateKey})
						}
					}
					tc.Channels = &cl
				}
				tlist = append(tlist, tc)
			}
			comp.Triggers = &tlist
		}
		cfg.Companions = append(cfg.Companions, comp)
	}

	if r := rows.repeater; r != nil {
		cfg.Repeater = &config.RepeaterConfig{
			Name:                r.Name,
			PrivateKey:          r.PrivateKey,
			Latitude:            r.Latitude,
			Longitude:           r.Longitude,
			AdvertInterval:      r.AdvertInterval,
			FloodAdvertInterval: r.FloodAdvertInterval,
			DisableFwd:          r.DisableFwd,
			FloodMax:            r.FloodMax,
			FloodMaxUnscoped:    r.FloodMaxUnscoped,
			FloodMaxAdvert:      r.FloodMaxAdvert,
			LoopDetect:          r.LoopDetect,
			PathHashSize:        r.PathHashSize,
			TxDelayFactor:       r.TxDelayFactor,
			DirectTxDelayFactor: r.DirectTxDelayFactor,
			RxDelayBase:         r.RxDelayBase,
			MultiAcks:           r.MultiAcks,
			DefaultRegion:       r.DefaultRegion,
			HomeRegion:          r.HomeRegion,
			AdminPassword:       r.AdminPassword,
			GuestPassword:       r.GuestPassword,
			OwnerInfo:           r.OwnerInfo,
		}
		for _, rg := range r.Regions {
			cfg.Repeater.Regions = append(cfg.Repeater.Regions, config.RepeaterRegion{Name: rg.Name, DenyFlood: rg.DenyFlood})
		}
	}

	if hasMqttConfig(rows.mqtt, rows.brokers) {
		mq := rows.mqtt
		m := &config.MqttConfig{
			Enabled:        mq.Enabled,
			IataCode:       mq.IataCode,
			StatusInterval: mq.StatusInterval,
			Owner:          mq.Owner,
			Email:          mq.Email,
		}
		if mq.NodeCompanionID != nil {
			if name, ok := idToName[*mq.NodeCompanionID]; ok {
				m.Node = &name
			}
		}
		for _, b := range rows.brokers {
			m.Brokers = append(m.Brokers, config.BrokerConfig{
				Name:                  b.Name,
				Enabled:               b.Enabled,
				Dedup:                 b.Dedup,
				Transport:             b.Transport,
				Host:                  b.Host,
				Port:                  b.Port,
				PacketTopic:           ptrToStr(b.PacketTopic),
				StatusTopic:           ptrToStr(b.StatusTopic),
				DisallowedPacketTypes: b.DisallowedPacketTypes,
				RetainStatus:          b.RetainStatus,
				TlsEnabled:            b.TLSEnabled,
				TlsInsecure:           b.TLSInsecure,
				AuthType:              b.AuthType,
				Username:              b.Username,
				Password:              b.Password,
				Path:                  b.Path,
				Audience:              b.Audience,
			})
		}
		cfg.Mqtt = m
	}
	return cfg
}

// readConfigFromTables assembles a *config.Config from the relational schema.
func readConfigFromTables(ctx context.Context, st *store.Store) (*config.Config, error) {
	rows, err := loadConfigRows(ctx, st)
	if err != nil {
		return nil, err
	}
	return assembleFromRows(rows), nil
}

// hasMqttConfig keeps an empty Mqtt block from being fabricated, which the runtime would treat as present.
func hasMqttConfig(mq *store.MqttSettings, brokers []store.Broker) bool {
	if len(brokers) > 0 {
		return true
	}
	return mq.Enabled != nil || mq.NodeCompanionID != nil || mq.IataCode != nil ||
		mq.StatusInterval != nil || mq.Owner != nil || mq.Email != nil
}

// writeConfigToTables MUST be called inside store.WriteSync — it issues many writes.
func writeConfigToTables(ctx context.Context, st *store.Store, cfg *config.Config) error {
	// The connection string is what Setup switches on, so the stored backend is derived from it
	// rather than trusted: an imported config that names one and points at another must not persist
	// the contradiction.
	connType := "kiss"
	if scheme, _, ok := config.ParseConnection(strDeref(cfg.Connection)); ok && scheme != "serial" && scheme != "tcp" {
		connType = scheme
	}
	if err := st.Settings.Set(ctx, &store.Settings{
		LogLevel:       cfg.LogLevel,
		ConnectionType: connType,
		Connection:     cfg.Connection,
		BaudRate:       cfg.BaudRate,
		SPIBoard:       cfg.SPIBoard,
		Freq:           cfg.Freq,
		BW:             cfg.Bw,
		SF:             u8ToIntPtr(cfg.SF),
		CR:             u8ToIntPtr(cfg.CR),
		TX:             u8ToIntPtr(cfg.TX),
		ListenAddr:     cfg.ListenAddr,
		MapProvider:    cfg.MapProviderOr(),
		MapDarkStyle:   cfg.MapDarkStyleOr(),
		MapTileKey:     cfg.MapTileKey,
		ModemToken:     cfg.ModemToken,
		PathHashSize:   cfg.PathHashSize,
		DutyCyclePct:   cfg.DutyCycle,
		SetupComplete:  cfg.SetupComplete != nil && *cfg.SetupComplete,
	}); err != nil {
		return err
	}

	existing, err := st.Companions.List(ctx)
	if err != nil {
		return err
	}
	byName := make(map[string]store.Companion, len(existing))
	for _, c := range existing {
		byName[c.Name] = c
	}

	keep := make(map[int64]bool)
	nameToID := make(map[string]int64, len(cfg.Companions))
	for _, cc := range cfg.Companions {
		pub, _ := config.PubKeyHexFromSeed(cc.PrivateKey)
		row := store.Companion{
			Name:           cc.Name,
			PrivateKey:     cc.PrivateKey,
			PubKey:         pub,
			Latitude:       cc.Latitude,
			Longitude:      cc.Longitude,
			AdvertInterval: cc.AdvertInterval,
			PathHashSize:   cc.PathHashSize,
			DMPolicy:       cc.DMPolicyOrDefault(),
			DMAllow:        ptrToSlice(cc.DMAllow),

			TelemBase: config.TelemetryModeOrDefault(cc.TelemetryBase),
			TelemLoc:  config.TelemetryModeOrDefault(cc.TelemetryLocation),
			TelemEnv:  config.TelemetryModeOrDefault(cc.TelemetryEnvironment),
		}
		if prev, ok := byName[cc.Name]; ok {
			row.ID = prev.ID
			if err := st.Companions.Update(ctx, &row); err != nil {
				return err
			}
		} else if err := st.Companions.Create(ctx, &row); err != nil {
			return err
		}
		keep[row.ID] = true
		nameToID[cc.Name] = row.ID

		if err := replaceCompanionChildren(ctx, st, row.ID, cc); err != nil {
			return err
		}
	}
	for _, c := range existing {
		if !keep[c.ID] {
			if err := st.Companions.Delete(ctx, c.ID); err != nil {
				return err
			}
		}
	}

	if err := writeRepeater(ctx, st, cfg); err != nil {
		return err
	}

	return writeMqtt(ctx, st, cfg, nameToID)
}

// writeRepeater clears the row when no repeater is configured; the pubkey column is derived from the seed.
func writeRepeater(ctx context.Context, st *store.Store, cfg *config.Config) error {
	if cfg.Repeater == nil {
		return st.Repeater.Clear(ctx)
	}
	r := cfg.Repeater
	pub, _ := config.PubKeyHexFromSeed(r.PrivateKey)
	var regions []store.RepeaterRegion
	for _, rg := range r.Regions {
		regions = append(regions, store.RepeaterRegion{Name: rg.Name, DenyFlood: rg.DenyFlood})
	}
	return st.Repeater.Set(ctx, &store.Repeater{
		Name:                r.Name,
		PrivateKey:          r.PrivateKey,
		PubKey:              pub,
		Latitude:            r.Latitude,
		Longitude:           r.Longitude,
		AdvertInterval:      r.AdvertInterval,
		FloodAdvertInterval: r.FloodAdvertInterval,
		DisableFwd:          r.DisableFwd,
		FloodMax:            r.FloodMax,
		FloodMaxUnscoped:    r.FloodMaxUnscoped,
		FloodMaxAdvert:      r.FloodMaxAdvert,
		LoopDetect:          r.LoopDetect,
		PathHashSize:        r.PathHashSize,
		TxDelayFactor:       r.TxDelayFactor,
		DirectTxDelayFactor: r.DirectTxDelayFactor,
		RxDelayBase:         r.RxDelayBase,
		MultiAcks:           r.MultiAcks,
		DefaultRegion:       r.DefaultRegion,
		HomeRegion:          r.HomeRegion,
		AdminPassword:       r.AdminPassword,
		GuestPassword:       r.GuestPassword,
		OwnerInfo:           r.OwnerInfo,
		Regions:             regions,
	})
}

// replaceCompanionChildren writes channels first, so triggers can resolve their references to the fresh channel ids.
func replaceCompanionChildren(ctx context.Context, st *store.Store, companionID int64, cc config.CompanionConfig) error {
	oldChans, err := st.Channels.ListByCompanion(ctx, companionID)
	if err != nil {
		return err
	}
	for _, oc := range oldChans {
		if err := st.Channels.Delete(ctx, oc.ID); err != nil {
			return err
		}
	}

	chanID := make(map[string]int64)
	if cc.Channels != nil {
		for _, ch := range *cc.Channels {
			cr := store.CompanionChannel{CompanionID: companionID, Name: ch.Name, PrivateKey: ch.PrivateKey}
			if err := st.Channels.Create(ctx, &cr); err != nil {
				return err
			}
			chanID[ch.Name] = cr.ID
		}
	}

	oldTrigs, err := st.Triggers.ListByCompanion(ctx, companionID)
	if err != nil {
		return err
	}
	for _, ot := range oldTrigs {
		if err := st.Triggers.Delete(ctx, ot.ID); err != nil {
			return err
		}
	}
	if cc.Triggers == nil {
		return nil
	}
	for _, tg := range *cc.Triggers {
		var chIDs []int64
		if tg.Channels != nil {
			for _, ref := range *tg.Channels {
				id, ok := chanID[ref.Name]
				if !ok {
					// A trigger channel outside the companion's list shouldn't happen; create it so a private key is never dropped.
					cr := store.CompanionChannel{CompanionID: companionID, Name: ref.Name, PrivateKey: ref.PrivateKey}
					if err := st.Channels.Create(ctx, &cr); err != nil {
						return err
					}
					chanID[ref.Name] = cr.ID
					id = cr.ID
				}
				chIDs = append(chIDs, id)
			}
		}
		tr := store.Trigger{
			CompanionID:        companionID,
			Type:               tg.Type,
			Template:           tg.Template,
			CharLimitBehaviour: tg.CharLimitBehaviour,
			MatchPatterns:      derefSlice(tg.Match),
			Contacts:           derefSlice(tg.Contacts),
			RetryTimeout:       tg.RetryTimeout,
			MaxRetries:         tg.MaxRetries,
			PathHashSize:       u8ToIntPtr(tg.PathHashSize),
			Schedule:           emptyToNil(tg.Schedule),
			URL:                tg.URL,
			FailoverPattern:    tg.FailoverPattern,
			FailoverTimeout:    tg.FailoverTimeout,
			Location:           locationToStore(tg.Location),
			Regions:            tg.Regions,
			ChannelIDs:         chIDs,
		}
		if err := st.Triggers.Create(ctx, &tr); err != nil {
			return err
		}
	}
	return nil
}

func writeMqtt(ctx context.Context, st *store.Store, cfg *config.Config, nameToID map[string]int64) error {
	var mq store.MqttSettings
	var brokers []config.BrokerConfig
	if cfg.Mqtt != nil {
		mq.Enabled = cfg.Mqtt.Enabled
		mq.IataCode = cfg.Mqtt.IataCode
		mq.StatusInterval = cfg.Mqtt.StatusInterval
		mq.Owner = cfg.Mqtt.Owner
		mq.Email = cfg.Mqtt.Email
		if cfg.Mqtt.Node != nil && *cfg.Mqtt.Node != "" {
			if id, ok := nameToID[*cfg.Mqtt.Node]; ok {
				mq.NodeCompanionID = &id
			}
		}
		brokers = cfg.Mqtt.Brokers
	}
	if err := st.Mqtt.Set(ctx, &mq); err != nil {
		return err
	}

	oldB, err := st.Brokers.List(ctx)
	if err != nil {
		return err
	}
	for _, b := range oldB {
		if err := st.Brokers.Delete(ctx, b.ID); err != nil {
			return err
		}
	}
	for _, b := range brokers {
		if err := st.Brokers.Create(ctx, &store.Broker{
			Name:                  b.Name,
			Enabled:               b.Enabled,
			Dedup:                 b.Dedup,
			Transport:             b.Transport,
			Host:                  b.Host,
			Port:                  b.Port,
			PacketTopic:           emptyToNil(b.PacketTopic),
			StatusTopic:           emptyToNil(b.StatusTopic),
			DisallowedPacketTypes: b.DisallowedPacketTypes,
			RetainStatus:          b.RetainStatus,
			TLSEnabled:            b.TlsEnabled,
			TLSInsecure:           b.TlsInsecure,
			AuthType:              b.AuthType,
			Username:              b.Username,
			Password:              b.Password,
			Path:                  b.Path,
			Audience:              b.Audience,
		}); err != nil {
			return err
		}
	}
	return nil
}

// initConfigTables is idempotent: it populates the relational config on first run, else just reads it back.
func initConfigTables(ctx context.Context, st *store.Store) (*config.Config, error) {
	if _, err := st.Settings.Get(ctx); err == nil {
		return readConfigFromTables(ctx, st)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("checking settings: %w", err)
	}

	// Not yet initialized. Migrate the legacy blob if present.
	if raw, berr := st.AppConfig.Get(ctx); berr == nil {
		cfg, perr := config.UnmarshalConfigJson([]byte(raw))
		if perr != nil {
			return nil, fmt.Errorf("parsing legacy config blob: %w", perr)
		}
		if err := cfg.EnsureNodeKeys(); err != nil {
			return nil, err
		}
		if err := persistToTables(ctx, st, cfg); err != nil {
			return nil, err
		}
		slog.Info("migrated legacy config blob into relational tables", "companions", len(cfg.Companions))
		return readConfigFromTables(ctx, st)
	} else if !errors.Is(berr, sql.ErrNoRows) {
		return nil, fmt.Errorf("reading legacy config blob: %w", berr)
	}

	// No tables, no blob: import a default-named file or bootstrap a default.
	if path := config.FindDefaultConfig(); path != "" {
		return importConfigFile(ctx, st, path)
	}
	def := config.DefaultConfig()
	if err := persistToTables(ctx, st, &def); err != nil {
		return nil, err
	}
	slog.Info("no config found; bootstrapped a quiet default, complete setup in the web UI")
	return readConfigFromTables(ctx, st)
}

// persistToTables writes a config through the store's single writer goroutine.
func persistToTables(ctx context.Context, st *store.Store, cfg *config.Config) error {
	var werr error
	st.WriteSync(func() { werr = writeConfigToTables(ctx, st, cfg) })
	return werr
}

func boolPtr(b bool) *bool { return &b }

func ptrToSlice(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}
