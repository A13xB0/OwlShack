package store

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

// Options are the one part of a sensor the store does not understand, so a round trip has to hand back exactly what was saved.
func TestSensorRepo_RoundTrip(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()

	rows := []Sensor{
		{Provider: "pisugar", Kind: "pisugar", Name: "UPS", Options: map[string]string{}},
		{Provider: "i2c", Kind: "shtc3", Name: "Air", Options: map[string]string{
			"address": "0x70",
		}},
	}
	st.WriteSync(func() {
		for i := range rows {
			if err := st.Sensors.Create(ctx, &rows[i]); err != nil {
				t.Fatalf("creating sensor %d: %v", i, err)
			}
		}
	})

	got, err := st.Sensors.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !reflect.DeepEqual(got, rows) {
		t.Fatalf("List() = %+v, want %+v", got, rows)
	}

	rows[1].Name = "Air outside"
	rows[1].Options = map[string]string{"address": "0x71"}
	st.WriteSync(func() {
		if err := st.Sensors.Update(ctx, &rows[1]); err != nil {
			t.Fatalf("Update: %v", err)
		}
	})
	got, err = st.Sensors.List(ctx)
	if err != nil {
		t.Fatalf("List after update: %v", err)
	}
	if !reflect.DeepEqual(got, rows) {
		t.Fatalf("after update List() = %+v, want %+v", got, rows)
	}

	st.WriteSync(func() {
		if err := st.Sensors.Delete(ctx, rows[0].ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	})
	got, err = st.Sensors.List(ctx)
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(got) != 1 || got[0].ID != rows[1].ID {
		t.Fatalf("after delete List() = %+v, want only the second sensor", got)
	}
}

// A sensor with no options must read back as an empty map, not nil: the provider indexes it.
func TestSensorRepo_EmptyOptionsReadBackEmpty(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()

	s := Sensor{Provider: "pisugar", Kind: "pisugar", Name: "UPS"}
	st.WriteSync(func() {
		if err := st.Sensors.Create(ctx, &s); err != nil {
			t.Fatalf("Create: %v", err)
		}
	})
	got, err := st.Sensors.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got[0].Options == nil {
		t.Fatal("Options came back nil; a provider reading one would panic on a nil map write")
	}
	if len(got[0].Options) != 0 {
		t.Fatalf("Options = %v, want empty", got[0].Options)
	}
}

// An update or delete against a sensor that is gone must say so, as a missing row the API can answer 404, or the operator believes it landed.
func TestSensorRepo_UpdateAndDeleteRejectAMissingRow(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	var upd, del error
	st.WriteSync(func() {
		upd = st.Sensors.Update(t.Context(), &Sensor{ID: 999, Provider: "pisugar", Kind: "pisugar", Name: "x"})
		del = st.Sensors.Delete(t.Context(), 999)
	})
	for op, err := range map[string]error{"Update": upd, "Delete": del} {
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("%s of a sensor that does not exist gave %v, want a missing row", op, err)
		}
	}
}

// State is calibration learned over hours, so it must come back as written, as exactly one row however often it is saved.
func TestSensorRepo_StateRoundTrip(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()

	row := Sensor{Provider: "i2c", Kind: "bme680", Name: "Air", Options: map[string]string{}}
	st.WriteSync(func() {
		if err := st.Sensors.Create(ctx, &row); err != nil {
			t.Fatalf("creating sensor: %v", err)
		}
	})

	// Never stored is nil and no error: a sensor that has learned nothing yet is not a failure.
	got, err := st.Sensors.LoadState(ctx, row.ID)
	if err != nil || got != nil {
		t.Fatalf("LoadState before any save = %q, %v; want nil, nil", got, err)
	}

	st.WriteSync(func() {
		if err := st.Sensors.SaveState(ctx, row.ID, []byte(`{"version":1,"bandMax":[4.7,4.7]}`)); err != nil {
			t.Fatalf("SaveState: %v", err)
		}
		if err := st.Sensors.SaveState(ctx, row.ID, []byte(`{"version":1,"bandMax":[4.8,4.8]}`)); err != nil {
			t.Fatalf("SaveState again: %v", err)
		}
	})

	got, err = st.Sensors.LoadState(ctx, row.ID)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if string(got) != `{"version":1,"bandMax":[4.8,4.8]}` {
		t.Errorf("LoadState = %q, want the second save", got)
	}
	var rows int
	if err := st.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sensor_state").Scan(&rows); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("sensor_state holds %d rows after two saves, want 1: this is calibration, not a series", rows)
	}

	// A deleted sensor takes its state with it, or the next sensor to take that id inherits it.
	st.WriteSync(func() {
		if err := st.Sensors.Delete(ctx, row.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	})
	if got, err = st.Sensors.LoadState(ctx, row.ID); err != nil || got != nil {
		t.Errorf("after deleting the sensor its state is %q, %v; want nil, nil", got, err)
	}

	// A sensor saves as it closes, and a deleted one closes after its row has gone.
	st.WriteSync(func() {
		if err := st.Sensors.SaveState(ctx, row.ID, []byte(`{"version":1}`)); err != nil {
			t.Errorf("saving a deleted sensor's state: %v, want it skipped", err)
		}
	})
	if got, err = st.Sensors.LoadState(ctx, row.ID); err != nil || got != nil {
		t.Errorf("a deleted sensor's state was written back: %q, %v", got, err)
	}
}

// The map keys on node kind and id, so a repeater's rows outlive it unless Clear takes them, and the next repeater created publishes them.
func TestRepeaterClear_TakesItsChannelMap(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()

	s := Sensor{Provider: "pisugar", Kind: "pisugar", Name: "UPS", Options: map[string]string{}}
	c := Companion{Name: "home"}
	row := []TelemetryMapEntry{{Channel: 2, LPPType: 116, SensorID: 0, Metric: "voltage"}}
	st.WriteSync(func() {
		if err := st.Sensors.Create(ctx, &s); err != nil {
			t.Fatal(err)
		}
		if err := st.Companions.Create(ctx, &c); err != nil {
			t.Fatal(err)
		}
		if err := st.Repeater.Set(ctx, &Repeater{Name: "rep"}); err != nil {
			t.Fatal(err)
		}
		row[0].SensorID = s.ID
		for _, node := range []TelemetryNode{{Kind: NodeKindRepeater, ID: RepeaterNodeID}, {Kind: NodeKindCompanion, ID: c.ID}} {
			if err := st.TelemetryMap.ReplaceForNode(ctx, node, row); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.Repeater.Clear(ctx); err != nil {
			t.Fatal(err)
		}
	})

	left, err := st.TelemetryMap.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Node.Kind != NodeKindCompanion {
		t.Fatalf("after clearing the repeater the map holds %+v, want the companion's row alone", left)
	}
}

// The hub's check gives the reason; the index holds against any other writer, folding case as the hub does.
func TestSensorRepo_RefusesASecondSensorOfOneName(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()
	add := func(name string) error {
		var err error
		st.WriteSync(func() {
			err = st.Sensors.Create(ctx, &Sensor{Provider: "pisugar", Kind: "pisugar", Name: name, Options: map[string]string{}})
		})
		return err
	}
	if err := add("UPS"); err != nil {
		t.Fatal(err)
	}
	if err := add("ups"); err == nil {
		t.Error("a second sensor called ups was stored beside UPS")
	}
	if err := add("UPS 2"); err != nil {
		t.Errorf("a different name was refused, so the refusal above proves nothing: %v", err)
	}
}

// A value the code would read as deny, or a map row for no kind of node, is refused rather than stored.
func TestMigrateV15_ChecksTheClosedColumns(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()
	if _, err := st.db.ExecContext(ctx, `INSERT INTO sensors (id, provider, kind, name) VALUES (1, 'pisugar', 'pisugar', 'UPS')`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ bad, good string }{
		{
			`INSERT INTO companions (name, private_key, pubkey, telem_base) VALUES ('a', '', '', 'everyone')`,
			`INSERT INTO companions (name, private_key, pubkey, telem_base) VALUES ('b', '', '', 'contacts')`,
		},
		{
			`INSERT INTO telemetry_map (node_kind, node_id, channel, lpp_type, sensor_id, metric) VALUES ('phone', 1, 2, 116, 1, 'voltage')`,
			`INSERT INTO telemetry_map (node_kind, node_id, channel, lpp_type, sensor_id, metric) VALUES ('companion', 1, 2, 116, 1, 'voltage')`,
		},
	} {
		if _, err := st.db.ExecContext(ctx, tc.bad); err == nil {
			t.Errorf("stored: %s", tc.bad)
		}
		if _, err := st.db.ExecContext(ctx, tc.good); err != nil {
			t.Errorf("the good row was refused too, so the refusal proves nothing: %v", err)
		}
	}
}
