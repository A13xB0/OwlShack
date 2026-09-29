package store

import (
	"reflect"
	"testing"
)

func mapTestSensor(t *testing.T, st *Store, name string) int64 {
	t.Helper()
	s := Sensor{Provider: "i2c", Kind: "shtc3", Name: name, Options: map[string]string{"address": "0x70"}}
	st.WriteSync(func() {
		if err := st.Sensors.Create(t.Context(), &s); err != nil {
			t.Fatalf("creating the sensor to map: %v", err)
		}
	})
	return s.ID
}

func mapTestCompanion(t *testing.T, st *Store, name string) int64 {
	t.Helper()
	c := Companion{Name: name}
	st.WriteSync(func() {
		if err := st.Companions.Create(t.Context(), &c); err != nil {
			t.Fatalf("creating the companion: %v", err)
		}
	})
	return c.ID
}

var repeaterNode = TelemetryNode{Kind: NodeKindRepeater, ID: RepeaterNodeID}

func TestTelemetryMapRepo_RoundTripsInSendOrder(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()
	id := mapTestSensor(t, st, "air")

	// Saved out of order on purpose: the map is sent by channel then type, whatever order the editor added rows in.
	st.WriteSync(func() {
		err := st.TelemetryMap.ReplaceForNode(ctx, repeaterNode, []TelemetryMapEntry{
			{Channel: 3, LPPType: 103, SensorID: id, Metric: "temperature"},
			{Channel: 2, LPPType: 104, SensorID: id, Metric: "humidity"},
			{Channel: 2, LPPType: 103, SensorID: id, Metric: "temperature"},
		})
		if err != nil {
			t.Fatalf("saving the map: %v", err)
		}
	})

	got, err := st.TelemetryMap.List(ctx)
	if err != nil {
		t.Fatalf("listing the map: %v", err)
	}
	want := []TelemetryMapEntry{
		{ID: got[0].ID, Node: repeaterNode, Channel: 2, LPPType: 103, SensorID: id, Metric: "temperature"},
		{ID: got[1].ID, Node: repeaterNode, Channel: 2, LPPType: 104, SensorID: id, Metric: "humidity"},
		{ID: got[2].ID, Node: repeaterNode, Channel: 3, LPPType: 103, SensorID: id, Metric: "temperature"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("map = %+v, want %+v", got, want)
	}
}

// Every node answers for itself, so one channel and type on two nodes is no clash and saving one must not disturb the other.
func TestTelemetryMapRepo_KeepsEachNodesMapApart(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()
	id := mapTestSensor(t, st, "air")
	one := TelemetryNode{Kind: NodeKindCompanion, ID: mapTestCompanion(t, st, "one")}
	two := TelemetryNode{Kind: NodeKindCompanion, ID: mapTestCompanion(t, st, "two")}

	st.WriteSync(func() {
		for _, node := range []TelemetryNode{repeaterNode, one, two} {
			err := st.TelemetryMap.ReplaceForNode(ctx, node, []TelemetryMapEntry{
				{Channel: 2, LPPType: 103, SensorID: id, Metric: "temperature"},
			})
			if err != nil {
				t.Fatalf("saving the map for %+v: %v", node, err)
			}
		}
	})

	rows, err := st.TelemetryMap.List(ctx)
	if err != nil {
		t.Fatalf("listing the map: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("%d rows, want one per node; channel 2 temperature is not a clash across nodes", len(rows))
	}

	// Clearing one node leaves the others exactly as they were.
	st.WriteSync(func() {
		if err := st.TelemetryMap.ReplaceForNode(ctx, one, nil); err != nil {
			t.Fatalf("clearing one companion's map: %v", err)
		}
	})
	rows, err = st.TelemetryMap.List(ctx)
	if err != nil {
		t.Fatalf("listing the map: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows after clearing one node, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Node == one {
			t.Fatalf("a row for the cleared node survived: %+v", r)
		}
	}
}

// The same channel and type twice would overwrite on the wire; the app validates first, this is the backstop.
func TestTelemetryMapRepo_RefusesTheSameChannelAndTypeTwiceOnOneNode(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()
	id := mapTestSensor(t, st, "air")

	var err error
	st.WriteSync(func() {
		err = st.TelemetryMap.ReplaceForNode(ctx, repeaterNode, []TelemetryMapEntry{
			{Channel: 2, LPPType: 103, SensorID: id, Metric: "temperature"},
			{Channel: 2, LPPType: 103, SensorID: id, Metric: "humidity"},
		})
	})
	if err == nil {
		t.Fatal("saved two rows for one channel and type")
	}

	rows, listErr := st.TelemetryMap.List(ctx)
	if listErr != nil {
		t.Fatalf("listing after the refusal: %v", listErr)
	}
	if len(rows) != 0 {
		t.Fatalf("a refused save left %d rows behind; the transaction did not roll back", len(rows))
	}
}

// ON DELETE CASCADE needs the foreign_keys pragma, set in the DSN a long way from here, so the cascade is checked.
func TestTelemetryMapRepo_DropsRowsWithTheirSensor(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()
	keep := mapTestSensor(t, st, "keep")
	drop := mapTestSensor(t, st, "drop")

	st.WriteSync(func() {
		err := st.TelemetryMap.ReplaceForNode(ctx, repeaterNode, []TelemetryMapEntry{
			{Channel: 2, LPPType: 103, SensorID: keep, Metric: "temperature"},
			{Channel: 3, LPPType: 103, SensorID: drop, Metric: "temperature"},
		})
		if err != nil {
			t.Fatalf("saving the map: %v", err)
		}
	})

	st.WriteSync(func() {
		if err := st.Sensors.Delete(ctx, drop); err != nil {
			t.Fatalf("deleting the sensor: %v", err)
		}
	})

	got, err := st.TelemetryMap.List(ctx)
	if err != nil {
		t.Fatalf("listing the map: %v", err)
	}
	if len(got) != 1 || got[0].SensorID != keep {
		t.Fatalf("map = %+v, want only the row for the sensor that is still there", got)
	}
}

// The map rows and the companion go together or not at all, or a failed delete leaves a companion that publishes nothing.
func TestCompanionDelete_KeepsTheMapWhenTheDeleteFails(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()
	id := mapTestSensor(t, st, "air")
	node := TelemetryNode{Kind: NodeKindCompanion, ID: mapTestCompanion(t, st, "home")}
	var err error
	st.WriteSync(func() {
		if err = st.TelemetryMap.ReplaceForNode(ctx, node, []TelemetryMapEntry{{Channel: 2, LPPType: 103, SensorID: id, Metric: "temperature"}}); err != nil {
			return
		}
		if _, err = st.db.ExecContext(ctx, `CREATE TRIGGER refuse BEFORE DELETE ON companions BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
			return
		}
		if st.Companions.Delete(ctx, node.ID) == nil {
			t.Error("the companion delete went through the trigger that refuses it")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.TelemetryMap.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("after a refused delete the map holds %d rows, want the companion's row still there", len(rows))
	}
}

// A companion's rows are cleared by its own delete, since a row may be the repeater's and there is nothing to cascade from.
func TestTelemetryMapRepo_DropsRowsWithTheirCompanion(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()
	id := mapTestSensor(t, st, "air")
	gone := TelemetryNode{Kind: NodeKindCompanion, ID: mapTestCompanion(t, st, "gone")}

	st.WriteSync(func() {
		for _, node := range []TelemetryNode{repeaterNode, gone} {
			err := st.TelemetryMap.ReplaceForNode(ctx, node, []TelemetryMapEntry{
				{Channel: 2, LPPType: 103, SensorID: id, Metric: "temperature"},
			})
			if err != nil {
				t.Fatalf("saving the map: %v", err)
			}
		}
	})

	st.WriteSync(func() {
		if err := st.Companions.Delete(ctx, gone.ID); err != nil {
			t.Fatalf("deleting the companion: %v", err)
		}
	})

	rows, err := st.TelemetryMap.List(ctx)
	if err != nil {
		t.Fatalf("listing the map: %v", err)
	}
	if len(rows) != 1 || rows[0].Node != repeaterNode {
		t.Fatalf("map = %+v, want only the repeater's row", rows)
	}
}
