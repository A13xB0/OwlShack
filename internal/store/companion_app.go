package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CompanionApp is a companion's connection for MeshCore companion apps, and the preferences an app sets on it.
type CompanionApp struct {
	CompanionID int64
	// Port 0 means no app connection; Bind "" listens on every address.
	Port           int
	Bind           string
	AllowKeyExport bool

	ManualAdd      byte
	AutoAddConfig  byte
	AutoAddMaxHops byte
	AdvLocPolicy   byte
	MultiAcks      byte
	RxDelayBaseMs  uint32
	// AirtimeFactorMs is the firmware's airtime_factor x1000; 1000 is its default of 1.0.
	AirtimeFactorMs uint32
	BLEPin          uint32
}

// DefaultCompanionApp is a companion no app has configured: no port, firmware's default preferences.
func DefaultCompanionApp(companionID int64) CompanionApp {
	return CompanionApp{CompanionID: companionID, AirtimeFactorMs: 1000}
}

type CompanionAppRepo struct{ db *sql.DB }

const companionAppCols = `companion_id, port, bind, allow_key_export, manual_add, autoadd_config, autoadd_max_hops,
	adv_loc_policy, multi_acks, rx_delay_base_ms, airtime_factor_ms, ble_pin`

func scanCompanionApp(s interface{ Scan(...any) error }) (CompanionApp, error) {
	var a CompanionApp
	err := s.Scan(&a.CompanionID, &a.Port, &a.Bind, &a.AllowKeyExport, &a.ManualAdd, &a.AutoAddConfig, &a.AutoAddMaxHops,
		&a.AdvLocPolicy, &a.MultiAcks, &a.RxDelayBaseMs, &a.AirtimeFactorMs, &a.BLEPin)
	return a, err
}

func (r *CompanionAppRepo) List(ctx context.Context) ([]CompanionApp, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+companionAppCols+` FROM companion_app ORDER BY companion_id`)
	if err != nil {
		return nil, fmt.Errorf("listing companion apps: %w", err)
	}
	defer rows.Close()
	var out []CompanionApp
	for rows.Next() {
		a, err := scanCompanionApp(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning companion app: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Get returns the companion's row, or the defaults when it has none.
func (r *CompanionAppRepo) Get(ctx context.Context, companionID int64) (CompanionApp, error) {
	a, err := scanCompanionApp(r.db.QueryRowContext(ctx, `SELECT `+companionAppCols+` FROM companion_app WHERE companion_id = ?`, companionID))
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultCompanionApp(companionID), nil
	}
	if err != nil {
		return CompanionApp{}, fmt.Errorf("getting companion app: %w", err)
	}
	return a, nil
}

// SetConnection writes the operator's connection fields and leaves the app's preferences alone.
func (r *CompanionAppRepo) SetConnection(ctx context.Context, companionID int64, port int, bind string, allowKeyExport bool) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO companion_app (companion_id, port, bind, allow_key_export) VALUES (?, ?, ?, ?)
		ON CONFLICT(companion_id) DO UPDATE SET port = excluded.port, bind = excluded.bind, allow_key_export = excluded.allow_key_export`,
		companionID, port, bind, allowKeyExport)
	if err != nil {
		return fmt.Errorf("setting companion app connection: %w", err)
	}
	return nil
}

// SetPrefs writes what an app set and leaves the operator's connection fields alone.
func (r *CompanionAppRepo) SetPrefs(ctx context.Context, a CompanionApp) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO companion_app (companion_id, manual_add, autoadd_config, autoadd_max_hops, adv_loc_policy, multi_acks,
			rx_delay_base_ms, airtime_factor_ms, ble_pin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(companion_id) DO UPDATE SET
			manual_add = excluded.manual_add, autoadd_config = excluded.autoadd_config,
			autoadd_max_hops = excluded.autoadd_max_hops, adv_loc_policy = excluded.adv_loc_policy,
			multi_acks = excluded.multi_acks, rx_delay_base_ms = excluded.rx_delay_base_ms,
			airtime_factor_ms = excluded.airtime_factor_ms, ble_pin = excluded.ble_pin`,
		a.CompanionID, a.ManualAdd, a.AutoAddConfig, a.AutoAddMaxHops, a.AdvLocPolicy, a.MultiAcks,
		a.RxDelayBaseMs, a.AirtimeFactorMs, a.BLEPin)
	if err != nil {
		return fmt.Errorf("setting companion app preferences: %w", err)
	}
	return nil
}
