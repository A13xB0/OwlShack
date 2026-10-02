package appserver

import (
	"errors"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/companion"
)

const (
	maxSignData        = 8 * 1024
	maxChannelDataLen  = companion.MaxFrameSize - 9 // MAX_CHANNEL_DATA_LENGTH
	dataTypeReserved   = 0x0000
	maxAutoAddHops     = 64
	maxAdvertNameBytes = 31
)

// RepeatFreqRanges are the firmware's default ranges a companion may repeat on, in kHz.
var RepeatFreqRanges = []companion.FreqRange{{LowerFreq: 433000, UpperFreq: 433000}, {LowerFreq: 869495, UpperFreq: 869495}, {LowerFreq: 918000, UpperFreq: 918000}}

type encodable interface{ ToBytes() []byte }

func (s *session) reply(r encodable) { s.send(r.ToBytes()) }
func (s *session) ok()               { s.reply(companion.OkResponse{}) }
func (s *session) refuse(code byte) {
	s.reply(companion.ErrResponse{ErrorCode: code, HasErrorCode: true})
}

// okOr answers OK, or the error's code.
func (s *session) okOr(err error) {
	if err != nil {
		s.refuse(errCode(err))
		return
	}
	s.ok()
}

func (s *session) currentScope() Scope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scope
}

func (s *session) target() byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.targetVer
}

// handle answers one command as handleCmdFrame does (companion_radio/MyMesh.cpp:1022-2013).
func (s *session) handle(frame []byte) {
	cmd, err := companion.ParseCommand(frame)
	if err != nil {
		var ce *companion.CommandError
		if errors.As(err, &ce) {
			s.refuse(ce.ErrCode)
		} else {
			s.refuse(companion.ErrCodeUnsupportedCmd)
		}
		return
	}
	d := s.port.currentDevice()
	if d == nil {
		s.refuse(companion.ErrCodeBadState) // the companion is not running, as while the radio reconnects
		return
	}

	switch c := cmd.(type) {
	case companion.DeviceQueryCommand:
		s.mu.Lock()
		s.targetVer = c.AppTargetVersion
		s.mu.Unlock()
		s.reply(d.DeviceInfo())
	case companion.AppStartCommand:
		s.mu.Lock()
		s.name = c.AppName
		s.mu.Unlock()
		s.reply(d.SelfInfo())
	case companion.HasConnectionCommand:
		if d.HasConnection(c.PublicKey) {
			s.ok()
		} else {
			s.refuse(companion.ErrCodeNotFound)
		}
	case companion.LogoutCommand:
		d.Logout(c.PublicKey)
		s.ok()
	case companion.GetDeviceTimeCommand:
		s.reply(companion.CurrTimeResponse{Timestamp: uint32(time.Now().Unix())})
	case companion.SetDeviceTimeCommand:
		// The host's clock is set by NTP; a time at or ahead of it is accepted and left alone, an earlier one refused as the firmware does.
		if int64(c.EpochSecs) >= time.Now().Unix() {
			s.ok()
		} else {
			s.refuse(companion.ErrCodeIllegalArg)
		}
	case companion.GetBattAndStorageCommand:
		s.reply(d.BattAndStorage())
	case companion.GetStatsCommand:
		s.reply(d.Stats(c.StatsType))
	case companion.RebootCommand:
		s.close() // nothing restarts: the app reconnects to a companion that never went away
	case companion.FactoryResetCommand:
		s.refuse(companion.ErrCodeFileIoError) // an app may not wipe a companion
	case companion.SetDevicePinCommand:
		if c.Pin != 0 && (c.Pin < 100000 || c.Pin > 999999) {
			s.refuse(companion.ErrCodeIllegalArg)
			return
		}
		s.setPrefs(d, func(p *Prefs) { p.BLEPin = c.Pin })
	case companion.GetCustomVarsCommand:
		s.reply(companion.CustomVarsResponse{})
	case companion.SetCustomVarCommand:
		s.refuse(companion.ErrCodeIllegalArg) // no sensor settings to set
	case companion.GetAllowedRepeatFreqCommand:
		s.reply(companion.AllowedRepeatFreqResponse{Ranges: RepeatFreqRanges})

	case companion.SetAdvertNameCommand:
		s.okOr(d.SetName(meshcore.TruncateUTF8(c.Name, maxAdvertNameBytes)))
	case companion.SetAdvertLatLonCommand:
		if c.Latitude > 90e6 || c.Latitude < -90e6 || c.Longitude > 180e6 || c.Longitude < -180e6 {
			s.refuse(companion.ErrCodeIllegalArg)
			return
		}
		s.okOr(d.SetLatLon(c.Latitude, c.Longitude))
	case companion.SendSelfAdvertCommand:
		s.okOr(d.SendSelfAdvert(c.Flood))
	case companion.SetRadioParamsCommand:
		s.setRadioParams(c)
	case companion.SetTxPowerCommand:
		// The radio is shared: a valid power is acknowledged and changes nothing, so the rest of the app's save goes through.
		if p := int8(c.TxPower); p < -9 || p > int8(d.SelfInfo().MaxTxPower) {
			s.refuse(companion.ErrCodeIllegalArg)
		} else {
			s.ok()
		}
	case companion.SetTuningParamsCommand:
		s.setPrefs(d, func(p *Prefs) {
			p.RxDelayBaseMs, p.AirtimeFactorMs = milli(c.RxDelayBase), milli(c.AirtimeFactor)
		})
	case companion.GetTuningParamsCommand:
		p := d.Prefs()
		s.reply(companion.TuningParamsResponse{RxDelayBase: float32(p.RxDelayBaseMs) / 1000, AirtimeFactor: float32(p.AirtimeFactorMs) / 1000})
	case companion.SetOtherParamsCommand:
		s.setPrefs(d, func(p *Prefs) {
			p.ManualAdd = c.ManualAddContacts
			if c.HasTelemetryModes {
				p.TelemetryModes = (c.TelemetryModeEnvironment&3)<<4 | (c.TelemetryModeLocation&3)<<2 | c.TelemetryModeBase&3
			}
			if c.HasAdvertLocPolicy {
				p.AdvLocPolicy = c.AdvertLocPolicy
			}
			if c.HasMultiAcks {
				p.MultiAcks = c.MultiAcks
			}
		})
	case companion.SetPathHashModeCommand:
		s.okOr(d.SetPathHashMode(c.Mode))
	case companion.SetAutoAddConfigCommand:
		s.setPrefs(d, func(p *Prefs) {
			p.AutoAddConfig = c.Config
			if !c.OmitMaxHops {
				p.AutoAddMaxHops = min(c.MaxHops, maxAutoAddHops)
			}
		})
	case companion.GetAutoAddConfigCommand:
		p := d.Prefs()
		s.reply(companion.AutoAddConfigResponse{Config: p.AutoAddConfig, MaxHops: p.AutoAddMaxHops})
	case companion.ExportPrivateKeyCommand:
		if key, ok := d.PrivateKey(); ok {
			s.reply(companion.PrivateKeyResponse{PrivateKey: key})
		} else {
			s.reply(companion.DisabledResponse{})
		}
	case companion.ImportPrivateKeyCommand:
		s.reply(companion.DisabledResponse{}) // identities change in OwlShack, never from an app
	case companion.SignStartCommand:
		s.mu.Lock()
		s.signing, s.signData = true, nil
		s.mu.Unlock()
		s.reply(companion.SignStartResponse{MaxSignDataLen: maxSignData})
	case companion.SignDataCommand:
		s.signAppend(c.Data)
	case companion.SignFinishCommand:
		s.signFinish(d)

	case companion.GetContactsCommand:
		s.sendContacts(d, c)
	case companion.GetContactByKeyCommand:
		if ct, ok := d.Contact(c.PublicKey); ok {
			s.reply(ct)
		} else {
			s.refuse(companion.ErrCodeNotFound)
		}
	case companion.AddUpdateContactCommand:
		s.okOr(d.PutContact(c))
	case companion.RemoveContactCommand:
		s.okOr(d.RemoveContact(c.PublicKey))
	case companion.ResetPathCommand:
		s.okOr(d.ResetPath(c.PublicKey))
	case companion.ShareContactCommand:
		s.okOr(d.ShareContact(c.PublicKey))
	case companion.ExportContactCommand:
		var advert []byte
		var err error
		if c.Self {
			advert, err = d.SelfAdvert()
		} else {
			advert, err = d.ExportContact(c.PublicKey)
		}
		if err != nil {
			s.refuse(errCode(err))
			return
		}
		s.reply(companion.ExportContactResponse{AdvertData: advert})
	case companion.ImportContactCommand:
		if err := d.ImportContact(c.AdvertData); err != nil {
			s.refuse(companion.ErrCodeIllegalArg)
		} else {
			s.ok()
		}
	case companion.GetAdvertPathCommand:
		if ap, ok := d.AdvertPath(c.PublicKey); ok {
			s.reply(ap)
		} else {
			s.refuse(companion.ErrCodeNotFound)
		}

	case companion.GetChannelCommand:
		if ch, ok := d.Channel(c.ChannelIdx); ok {
			s.reply(ch)
		} else {
			s.refuse(companion.ErrCodeNotFound)
		}
	case companion.SetChannelCommand:
		s.okOr(d.SetChannel(c.ChannelIdx, c.Name, c.Secret))

	case companion.SendTxtMsgCommand:
		s.sendText(d, c)
	case companion.SendChannelTxtMsgCommand:
		if c.TxtType != companion.TxtTypePlain {
			s.refuse(companion.ErrCodeUnsupportedCmd)
			return
		}
		s.okOr(d.SendChannelText(c.ChannelIdx, c.SenderTimestamp, c.Text, s.currentScope()))
	case companion.SendChannelDataCommand:
		if c.DataType == dataTypeReserved || len(c.Payload) > maxChannelDataLen {
			s.refuse(companion.ErrCodeIllegalArg)
			return
		}
		s.okOr(d.SendChannelData(c.ChannelIdx, c.Path, c.Flood, c.DataType, c.Payload, s.currentScope()))
	case companion.SyncNextMessageCommand:
		s.syncNext()
	case companion.SendRawDataCommand:
		s.okOr(d.SendRawData(c.Path, c.RawData))
	case companion.SendRawPacketCommand:
		s.okOr(d.SendRawPacket(c.Priority, c.Packet))
	case companion.SendControlDataCommand:
		s.okOr(d.SendControlData(c.ControlData))
	case companion.SetFloodScopeCommand:
		s.mu.Lock()
		switch {
		case c.Unscoped:
			s.scope.Set, s.scope.Unscoped = true, true
		case len(c.TransportKey) == meshcore.RegionKeySize:
			s.scope = Scope{Set: true, Region: meshcore.NewRegionFromKey("", meshcore.RegionKey(c.TransportKey))}
		default:
			s.scope = Scope{}
		}
		s.mu.Unlock()
		s.ok()
	case companion.SetDefaultFloodScopeCommand:
		if len(c.Key) != meshcore.RegionKeySize {
			s.okOr(d.SetDefaultScope("", nil))
			return
		}
		if c.Name == "" {
			s.refuse(companion.ErrCodeIllegalArg)
			return
		}
		s.okOr(d.SetDefaultScope(c.Name, c.Key))
	case companion.GetDefaultFloodScopeCommand:
		name, key := d.DefaultScope()
		s.reply(companion.DefaultFloodScopeResponse{Name: name, Key: key})
	case companion.SendTracePathCommand:
		s.sent(d.SendTrace(c.Tag, c.Auth, c.Flags, c.Path))

	case companion.SendLoginCommand:
		if _, ok := d.Contact(c.PublicKey); !ok {
			s.refuse(companion.ErrCodeNotFound)
			return
		}
		s.sent(d.Login(c.PublicKey, c.Password))
	case companion.SendStatusReqCommand:
		s.request(d, c.PublicKey, RequestStatus, nil)
	case companion.SendTelemetryReqCommand:
		if c.Self {
			self := d.SelfInfo().PublicKey
			resp := companion.PushTelemetryResp{LPPData: d.SelfTelemetry()}
			copy(resp.PubKeyPrefix[:], self[:6])
			s.reply(resp)
			return
		}
		s.request(d, c.PublicKey, RequestTelemetry, nil)
	case companion.SendBinaryReqCommand:
		s.request(d, c.PublicKey, RequestBinary, c.RequestData)
	case companion.SendPathDiscoveryReqCommand:
		s.request(d, c.PublicKey, RequestPathDiscovery, nil)
	case companion.SendAnonReqCommand:
		s.sent(d.Request(c.PublicKey, RequestAnon, c.RequestData, s.currentScope()))

	default:
		s.refuse(companion.ErrCodeUnsupportedCmd)
	}
}

func milli(v float32) uint32 { return uint32(float64(v)*1000 + 0.5) }

func (s *session) setPrefs(d Device, edit func(*Prefs)) {
	p := d.Prefs()
	edit(&p)
	s.okOr(d.SetPrefs(p))
}

// setRadioParams checks the firmware's ranges and acknowledges a valid change without making it: the radio is shared with every other node here.
func (s *session) setRadioParams(c companion.SetRadioParamsCommand) {
	if c.ClientRepeat && !repeatAllowed(c.Frequency) {
		s.refuse(companion.ErrCodeIllegalArg)
		return
	}
	if c.Frequency < 150000 || c.Frequency > 2500000 || c.SpreadFactor < 5 || c.SpreadFactor > 12 ||
		c.CodingRate < 5 || c.CodingRate > 8 || c.Bandwidth < 7000 || c.Bandwidth > 500000 {
		s.refuse(companion.ErrCodeIllegalArg)
		return
	}
	s.ok()
}

func repeatAllowed(freqKHz uint32) bool {
	for _, r := range RepeatFreqRanges {
		if freqKHz >= r.LowerFreq && freqKHz <= r.UpperFreq {
			return true
		}
	}
	return false
}

func (s *session) sent(resp companion.SentResponse, err error) {
	if err != nil {
		s.refuse(errCode(err))
		return
	}
	s.reply(resp)
}

func (s *session) request(d Device, key [32]byte, kind RequestKind, data []byte) {
	if _, ok := d.Contact(key); !ok {
		s.refuse(companion.ErrCodeNotFound)
		return
	}
	s.sent(d.Request(key, kind, data, s.currentScope()))
}

func (s *session) sendText(d Device, c companion.SendTxtMsgCommand) {
	key, ok := d.FindContact(c.PubKeyPrefix[:])
	if !ok {
		s.refuse(companion.ErrCodeNotFound)
		return
	}
	if c.TxtType != companion.TxtTypePlain && c.TxtType != companion.TxtTypeCLIData {
		s.refuse(companion.ErrCodeUnsupportedCmd)
		return
	}
	p := s.port
	s.sent(d.SendText(key, c.TxtType, c.Attempt, c.SenderTimestamp, c.Text, s.currentScope(), func(ack, rtt uint32) {
		p.event(Event{Push: &companion.Response{Code: companion.PushSendConfirmed, Data: companion.PushSendConfirmedResponse{AckCode: ack, RoundTrip: rtt}}})
	}))
}

// sendContacts streams the contact list as CONTACTS_START, one CONTACT each changed since the filter, then END_OF_CONTACTS with the newest change.
func (s *session) sendContacts(d Device, c companion.GetContactsCommand) {
	list := d.Contacts()
	s.reply(companion.ContactsStartResponse{Count: uint32(len(list)), HasCount: true})
	var newest uint32
	for _, ct := range list {
		if c.HasSince && ct.LastModified <= c.Since {
			continue
		}
		s.reply(ct)
		newest = max(newest, ct.LastModified)
	}
	s.reply(companion.EndOfContactsResponse{MostRecentLastmod: newest})
}

func (s *session) signAppend(data []byte) {
	s.mu.Lock()
	var code byte
	switch {
	case !s.signing:
		code = companion.ErrCodeBadState
	case len(s.signData)+len(data) > maxSignData:
		code = companion.ErrCodeTableFull
	default:
		s.signData = append(s.signData, data...)
	}
	s.mu.Unlock()
	if code != 0 {
		s.refuse(code)
		return
	}
	s.ok()
}

func (s *session) signFinish(d Device) {
	s.mu.Lock()
	signing, data := s.signing, s.signData
	s.signing, s.signData = false, nil
	s.mu.Unlock()
	if !signing {
		s.refuse(companion.ErrCodeBadState)
		return
	}
	sig, err := d.Sign(data)
	if err != nil {
		s.refuse(errCode(err))
		return
	}
	s.reply(companion.SignatureResponse{Signature: sig})
}

// syncNext hands the app its oldest queued message, in the frame its protocol version reads.
func (s *session) syncNext() {
	m, ok := s.port.queue.pop()
	if !ok {
		s.reply(companion.NoMoreMessagesResponse{})
		return
	}
	v3 := s.target() >= 3
	switch m.Kind {
	case MessageContact:
		if v3 {
			s.reply(companion.ContactMsgRecvV3Response{SNR: m.SNR, PubKeyPrefix: m.SenderPrefix, PathLen: m.PathLen, TxtType: m.TxtType, SenderPrefix: m.AuthorPrefix, SenderTimestamp: m.SenderTimestamp, Text: m.Text})
		} else {
			s.reply(companion.ContactMsgRecvResponse{PubKeyPrefix: m.SenderPrefix, PathLen: m.PathLen, TxtType: m.TxtType, SenderPrefix: m.AuthorPrefix, SenderTimestamp: m.SenderTimestamp, Text: m.Text})
		}
	case MessageChannel:
		if v3 {
			s.reply(companion.ChannelMsgRecvV3Response{SNR: m.SNR, ChannelIdx: byte(m.ChannelIdx), PathLen: m.PathLen, TxtType: m.TxtType, SenderTimestamp: m.SenderTimestamp, Text: m.Text})
		} else {
			s.reply(companion.ChannelMsgRecvResponse{ChannelIdx: byte(m.ChannelIdx), PathLen: m.PathLen, TxtType: m.TxtType, SenderTimestamp: m.SenderTimestamp, Text: m.Text})
		}
	case MessageChannelData:
		s.reply(companion.ChannelDataRecvResponse{SNR: m.SNR, ChannelIdx: int8(m.ChannelIdx), PathLen: m.PathLen, DataType: m.DataType, Data: m.Data})
	}
}
