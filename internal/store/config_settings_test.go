package store

import (
	"testing"
)

func strPtr(s string) *string   { return &s }
func intPtr2(i int) *int        { return &i }
func fltPtr(f float64) *float64 { return &f }

// Columns, placeholders and Scan destinations are listed three separate times, so only a round trip catches a column added to just two of them.
func TestSettings_RoundTripsEveryColumn(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()

	want := &Settings{
		LogLevel:       strPtr("debug"),
		ConnectionType: "spi",
		Connection:     strPtr("spi://SPI0.0"),
		BaudRate:       intPtr2(57600),
		SPIBoard:       strPtr("ultrapeaterzero-e22p"),
		Freq:           fltPtr(917.375),
		BW:             fltPtr(62.5),
		SF:             intPtr2(7),
		CR:             intPtr2(5),
		TX:             intPtr2(22),
		ListenAddr:     strPtr(":8081"),
		MapProvider:    "carto",
		MapDarkStyle:   "simplified",
		MapTileKey:     strPtr("tile-key"),
		ModemToken:     strPtr("s3cret"),
		PathHashSize:   intPtr2(2),
		DutyCyclePct:   fltPtr(12.5),
		SetupComplete:  true,
	}
	// Seed a different row first, so the assertions below run against the UPDATE arm of Set's
	// upsert. That is the arm every save after the first one takes, and a column left out of the
	// ON CONFLICT list is invisible to an insert-only test.
	if err := st.Settings.Set(ctx, &Settings{ConnectionType: "kiss", Connection: strPtr("serial:///dev/ttyACM0")}); err != nil {
		t.Fatalf("seed Set: %v", err)
	}
	if err := st.Settings.Set(ctx, want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := st.Settings.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	eqStr := func(field string, a, b *string) {
		t.Helper()
		switch {
		case a == nil && b == nil:
		case a == nil || b == nil:
			t.Errorf("%s: got %v, want %v", field, deref(b), deref(a))
		case *a != *b:
			t.Errorf("%s: got %q, want %q", field, *b, *a)
		}
	}
	eqStr("logLevel", want.LogLevel, got.LogLevel)
	eqStr("connection", want.Connection, got.Connection)
	eqStr("spiBoard", want.SPIBoard, got.SPIBoard)
	eqStr("listenAddr", want.ListenAddr, got.ListenAddr)
	eqStr("mapTileKey", want.MapTileKey, got.MapTileKey)
	eqStr("modemToken", want.ModemToken, got.ModemToken)
	if got.MapDarkStyle != want.MapDarkStyle {
		t.Errorf("mapDarkStyle: got %q, want %q", got.MapDarkStyle, want.MapDarkStyle)
	}
	if got.MapProvider != want.MapProvider {
		t.Errorf("mapProvider: got %q, want %q", got.MapProvider, want.MapProvider)
	}
	if got.ConnectionType != want.ConnectionType {
		t.Errorf("connectionType: got %q, want %q", got.ConnectionType, want.ConnectionType)
	}
	for _, c := range []struct {
		field string
		a, b  *int
	}{
		{"baudRate", want.BaudRate, got.BaudRate},
		{"sf", want.SF, got.SF},
		{"cr", want.CR, got.CR},
		{"tx", want.TX, got.TX},
		{"pathHashSize", want.PathHashSize, got.PathHashSize},
	} {
		if c.a == nil || c.b == nil || *c.a != *c.b {
			t.Errorf("%s: got %v, want %v", c.field, c.b, c.a)
		}
	}
	for _, c := range []struct {
		field string
		a, b  *float64
	}{
		{"freq", want.Freq, got.Freq},
		{"bw", want.BW, got.BW},
		{"dutyCyclePct", want.DutyCyclePct, got.DutyCyclePct},
	} {
		if c.a == nil || c.b == nil || *c.a != *c.b {
			t.Errorf("%s: got %v, want %v", c.field, c.b, c.a)
		}
	}
	if !got.SetupComplete {
		t.Error("setupComplete: got false, want true")
	}
}

// spi_board must stay NULL for KISS: the config layer rejects an spi:// connection whose board is unset, and "" would read as set.
func TestSettings_SPIBoardStaysNullForKiss(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()

	if err := st.Settings.Set(ctx, &Settings{
		ConnectionType: "kiss",
		Connection:     strPtr("serial:///dev/ttyACM0"),
		Freq:           fltPtr(917.375),
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := st.Settings.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SPIBoard != nil {
		t.Errorf("spiBoard = %q, want nil for a KISS connection", *got.SPIBoard)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
