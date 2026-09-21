// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zyvorai/nodra/internal/model"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresStore persists fleet state in PostgreSQL. Every read queries the
// database. There is no process-local fleet cache: two control-plane
// processes pointed at the same database see each other's writes.
// Delivery and DLQ queues remain internal/queue.PostgresQueue.
type PostgresStore struct {
	db   *sql.DB
	once sync.Once
	err  error
}

const occRetries = 8

var identRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type sqlCol struct {
	name string
	val  any
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Ping checks that the Postgres connection is still alive.
func (s *PostgresStore) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("postgres store is closed")
	}
	return s.db.PingContext(ctx)
}

func OpenPostgres(databaseURL string) (*PostgresStore, error) {
	if databaseURL == "" {
		return nil, errors.New("NODRA_DATABASE_URL is required for postgres store")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	db.SetConnMaxLifetime(30 * time.Minute)
	pingCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	migCtx, migCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer migCancel()
	if err = applyMigrations(migCtx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres migrate: %w", err)
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.db != nil {
			s.err = s.db.Close()
		}
	})
	return s.err
}

func (s *PostgresStore) Snapshot() model.State {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return model.State{}
	}
	defer tx.Rollback()
	var st model.State
	if st.Sites, err = queryJSON[model.Site](ctx, tx, `SELECT data FROM nodra_sites ORDER BY id`); err != nil {
		return model.State{}
	}
	if st.Devices, err = queryJSON[model.Device](ctx, tx, `SELECT data FROM nodra_devices ORDER BY id`); err != nil {
		return model.State{}
	}
	if st.Twins, err = queryJSON[model.Twin](ctx, tx, `SELECT data FROM nodra_twins ORDER BY device_id`); err != nil {
		return model.State{}
	}
	if st.Routes, err = queryJSON[model.Route](ctx, tx, `SELECT data FROM nodra_routes ORDER BY id`); err != nil {
		return model.State{}
	}
	if st.Deployments, err = queryJSON[model.Deployment](ctx, tx, `SELECT data FROM nodra_deployments ORDER BY id`); err != nil {
		return model.State{}
	}
	if st.PolicyPacks, err = queryJSON[model.PolicyPack](ctx, tx, `SELECT data FROM nodra_policy_packs ORDER BY id`); err != nil {
		return model.State{}
	}
	if st.OTACampaigns, err = queryJSON[model.OTACampaign](ctx, tx, `SELECT data FROM nodra_ota_campaigns ORDER BY id`); err != nil {
		return model.State{}
	}
	if st.Orgs, err = queryJSON[model.Org](ctx, tx, `SELECT data FROM nodra_orgs ORDER BY id`); err != nil {
		return model.State{}
	}
	if st.Alerts, err = queryJSON[model.Alert](ctx, tx, recentAlertsSQL); err != nil {
		return model.State{}
	}
	if st.Events, err = queryJSON[model.Event](ctx, tx, recentEventsSQL); err != nil {
		return model.State{}
	}
	for _, e := range st.Events {
		st.SeenEvents = append(st.SeenEvents, e.ID)
	}
	_ = tx.Commit()
	return st
}

const recentAlertsSQL = `
SELECT data FROM (
	SELECT data,
		CASE WHEN COALESCE(data->>'created_at', '') ~ '^[0-9]{4}-'
			THEN (data->>'created_at')::timestamptz
			ELSE TIMESTAMPTZ 'epoch' END AS created_at
	FROM nodra_alerts
	ORDER BY created_at DESC
	LIMIT 2000
) a
ORDER BY created_at ASC`

const recentEventsSQL = `
SELECT data FROM (
	SELECT data, event_time FROM nodra_events
	ORDER BY event_time DESC NULLS LAST
	LIMIT 2000
) e
ORDER BY event_time ASC NULLS FIRST`

func (s *PostgresStore) AddSite(v model.Site) error {
	return s.upsertDoc(context.Background(), "nodra_sites", "id", v.ID, v, []sqlCol{{"org_id", v.OrgID}})
}
func (s *PostgresStore) Site(id string) (model.Site, bool) {
	return getOne[model.Site](context.Background(), s.db, `SELECT data FROM nodra_sites WHERE id = $1`, id)
}
func (s *PostgresStore) UpdateSite(id string, fn func(*model.Site)) error {
	return occDoc(context.Background(), s, "nodra_sites", "id", id, func(v *model.Site) []sqlCol {
		fn(v)
		return []sqlCol{{"org_id", v.OrgID}}
	})
}
func (s *PostgresStore) Sites() []model.Site {
	out, _ := queryJSON[model.Site](context.Background(), s.db, `SELECT data FROM nodra_sites ORDER BY id`)
	return out
}

func (s *PostgresStore) AddDevice(v model.Device) error {
	return s.upsertDoc(context.Background(), "nodra_devices", "id", v.ID, v, []sqlCol{{"site_id", v.SiteID}})
}
func (s *PostgresStore) Devices() []model.Device {
	out, _ := queryJSON[model.Device](context.Background(), s.db, `SELECT data FROM nodra_devices ORDER BY id`)
	return out
}
func (s *PostgresStore) Device(id string) (model.Device, bool) {
	return getOne[model.Device](context.Background(), s.db, `SELECT data FROM nodra_devices WHERE id = $1`, id)
}

func (s *PostgresStore) Twin(deviceID string) (model.Twin, bool) {
	return getOne[model.Twin](context.Background(), s.db, `SELECT data FROM nodra_twins WHERE device_id = $1`, deviceID)
}
func (s *PostgresStore) TwinsForSite(siteID string) []model.Twin {
	out, _ := queryJSON[model.Twin](context.Background(), s.db, `SELECT data FROM nodra_twins WHERE site_id = $1 ORDER BY device_id`, siteID)
	return out
}
func (s *PostgresStore) SetTwin(v model.Twin) error {
	return s.upsertDoc(context.Background(), "nodra_twins", "device_id", v.DeviceID, v, twinCols(v))
}
func (s *PostgresStore) UpdateTwin(deviceID string, fn func(*model.Twin)) error {
	ctx := context.Background()
	for attempt := 0; attempt < occRetries; attempt++ {
		var raw []byte
		var rev int64
		err := s.db.QueryRowContext(ctx, `SELECT data, revision FROM nodra_twins WHERE device_id = $1`, deviceID).Scan(&raw, &rev)
		if errors.Is(err, sql.ErrNoRows) {
			v := model.Twin{DeviceID: deviceID}
			fn(&v)
			if v.DeviceID == "" {
				v.DeviceID = deviceID
			}
			b, mErr := json.Marshal(v)
			if mErr != nil {
				return mErr
			}
			res, execErr := s.db.ExecContext(ctx, `
INSERT INTO nodra_twins (device_id, data, revision, site_id, updated_at)
VALUES ($1, $2, 1, $3, $4)
ON CONFLICT (device_id) DO NOTHING`, v.DeviceID, b, v.SiteID, nullTime(v.UpdatedAt))
			if execErr != nil {
				return execErr
			}
			n, _ := res.RowsAffected()
			if n == 1 {
				return nil
			}
			continue
		}
		if err != nil {
			return err
		}
		var v model.Twin
		if err = json.Unmarshal(raw, &v); err != nil {
			return err
		}
		fn(&v)
		if v.DeviceID == "" {
			v.DeviceID = deviceID
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		n, err := s.updateRevision(ctx, "nodra_twins", "device_id", deviceID, rev, b, twinCols(v))
		if err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
	}
	return ErrConflict
}

func twinCols(v model.Twin) []sqlCol {
	return []sqlCol{{"site_id", v.SiteID}, {"updated_at", nullTime(v.UpdatedAt)}}
}

func (s *PostgresStore) AddRoute(v model.Route) error {
	return s.upsertDoc(context.Background(), "nodra_routes", "id", v.ID, v, []sqlCol{{"site_id", v.SiteID}})
}
func (s *PostgresStore) Routes() []model.Route {
	out, _ := queryJSON[model.Route](context.Background(), s.db, `SELECT data FROM nodra_routes ORDER BY id`)
	return out
}
func (s *PostgresStore) DeleteRoute(id string) error {
	return s.deleteID(context.Background(), "nodra_routes", "id", id)
}

func (s *PostgresStore) AddDeployment(v model.Deployment) error {
	return s.upsertDoc(context.Background(), "nodra_deployments", "id", v.ID, v, deploymentCols(v))
}
func (s *PostgresStore) Deployments() []model.Deployment {
	out, _ := queryJSON[model.Deployment](context.Background(), s.db, `SELECT data FROM nodra_deployments ORDER BY id`)
	return out
}
func (s *PostgresStore) Deployment(id string) (model.Deployment, bool) {
	return getOne[model.Deployment](context.Background(), s.db, `SELECT data FROM nodra_deployments WHERE id = $1`, id)
}
func (s *PostgresStore) UpdateDeployment(id string, fn func(*model.Deployment)) error {
	return occDoc(context.Background(), s, "nodra_deployments", "id", id, func(v *model.Deployment) []sqlCol {
		fn(v)
		return deploymentCols(*v)
	})
}
func (s *PostgresStore) DeleteDeployment(id string) error {
	return s.deleteID(context.Background(), "nodra_deployments", "id", id)
}

func deploymentCols(v model.Deployment) []sqlCol {
	return []sqlCol{{"site_id", v.SiteID}, {"updated_at", nullTime(v.UpdatedAt)}}
}

func (s *PostgresStore) AddPolicyPack(v model.PolicyPack) error {
	return s.upsertDoc(context.Background(), "nodra_policy_packs", "id", v.ID, v, policyCols(v))
}
func (s *PostgresStore) PolicyPacks() []model.PolicyPack {
	out, _ := queryJSON[model.PolicyPack](context.Background(), s.db, `SELECT data FROM nodra_policy_packs ORDER BY id`)
	return out
}
func (s *PostgresStore) PolicyPack(id string) (model.PolicyPack, bool) {
	return getOne[model.PolicyPack](context.Background(), s.db, `SELECT data FROM nodra_policy_packs WHERE id = $1`, id)
}
func (s *PostgresStore) UpdatePolicyPack(id string, fn func(*model.PolicyPack)) error {
	return occDoc(context.Background(), s, "nodra_policy_packs", "id", id, func(v *model.PolicyPack) []sqlCol {
		fn(v)
		return policyCols(*v)
	})
}
func (s *PostgresStore) DeletePolicyPack(id string) error {
	return s.deleteID(context.Background(), "nodra_policy_packs", "id", id)
}

func policyCols(v model.PolicyPack) []sqlCol {
	return []sqlCol{{"site_id", v.SiteID}, {"updated_at", nullTime(v.UpdatedAt)}}
}

func (s *PostgresStore) AddOTACampaign(v model.OTACampaign) error {
	return s.upsertDoc(context.Background(), "nodra_ota_campaigns", "id", v.ID, v, []sqlCol{{"updated_at", nullTime(v.UpdatedAt)}})
}
func (s *PostgresStore) OTACampaigns() []model.OTACampaign {
	out, _ := queryJSON[model.OTACampaign](context.Background(), s.db, `SELECT data FROM nodra_ota_campaigns ORDER BY id`)
	return out
}
func (s *PostgresStore) OTACampaign(id string) (model.OTACampaign, bool) {
	return getOne[model.OTACampaign](context.Background(), s.db, `SELECT data FROM nodra_ota_campaigns WHERE id = $1`, id)
}
func (s *PostgresStore) UpdateOTACampaign(id string, fn func(*model.OTACampaign)) error {
	return occDoc(context.Background(), s, "nodra_ota_campaigns", "id", id, func(v *model.OTACampaign) []sqlCol {
		fn(v)
		return []sqlCol{{"updated_at", nullTime(v.UpdatedAt)}}
	})
}

func (s *PostgresStore) AddOrg(v model.Org) error {
	return s.upsertDoc(context.Background(), "nodra_orgs", "id", v.ID, v, nil)
}
func (s *PostgresStore) Orgs() []model.Org {
	out, _ := queryJSON[model.Org](context.Background(), s.db, `SELECT data FROM nodra_orgs ORDER BY id`)
	return out
}
func (s *PostgresStore) Org(id string) (model.Org, bool) {
	return getOne[model.Org](context.Background(), s.db, `SELECT data FROM nodra_orgs WHERE id = $1`, id)
}
func (s *PostgresStore) UpdateOrg(id string, fn func(*model.Org)) error {
	return occDoc(context.Background(), s, "nodra_orgs", "id", id, func(v *model.Org) []sqlCol {
		fn(v)
		return nil
	})
}
func (s *PostgresStore) DeleteOrg(id string) error {
	return s.deleteID(context.Background(), "nodra_orgs", "id", id)
}

func (s *PostgresStore) AddAlert(v model.Alert) error {
	return s.upsertDoc(context.Background(), "nodra_alerts", "id", v.ID, v, []sqlCol{{"site_id", v.SiteID}})
}
func (s *PostgresStore) Alerts() []model.Alert {
	out, _ := queryJSON[model.Alert](context.Background(), s.db, recentAlertsSQL)
	return out
}
func (s *PostgresStore) ResolveAlert(id string) error {
	return occDoc(context.Background(), s, "nodra_alerts", "id", id, func(v *model.Alert) []sqlCol {
		v.Resolved = true
		return []sqlCol{{"site_id", v.SiteID}}
	})
}

func (s *PostgresStore) AddEvent(v model.Event) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	when := v.EventTime
	if when.IsZero() {
		when = v.CreatedAt
	}
	if when.IsZero() {
		when = time.Now().UTC()
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
INSERT INTO nodra_events (id, data, event_time, site_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (id) DO NOTHING`, v.ID, b, when, v.SiteID)
	if err != nil {
		return err
	}
	inserted, _ := res.RowsAffected()
	if _, err = tx.ExecContext(ctx, `INSERT INTO nodra_seen_events (id) VALUES ($1) ON CONFLICT DO NOTHING`, v.ID); err != nil {
		return err
	}
	if inserted == 0 {
		// Another replica already stored this event. Commit the idempotent
		// seen-id insert and do not treat the call as a new local append.
		return tx.Commit()
	}
	return tx.Commit()
}
func (s *PostgresStore) HasEvent(id string) bool {
	var one int
	err := s.db.QueryRowContext(context.Background(), `SELECT 1 FROM nodra_events WHERE id = $1`, id).Scan(&one)
	return err == nil
}
func (s *PostgresStore) EventsSince(t time.Time) []model.Event {
	out, _ := queryJSON[model.Event](context.Background(), s.db, `
SELECT data FROM (
	SELECT data, event_time FROM nodra_events
	WHERE event_time > $1
	ORDER BY event_time DESC
	LIMIT 2000
) e
ORDER BY event_time ASC`, t)
	return out
}

func (s *PostgresStore) rowRevision(table, idCol, id string) (int64, error) {
	table, idCol, err := checkedIdents(table, idCol)
	if err != nil {
		return 0, err
	}
	var rev int64
	err = s.db.QueryRowContext(context.Background(), fmt.Sprintf(`SELECT revision FROM %s WHERE %s = $1`, table, idCol), id).Scan(&rev)
	return rev, err
}

func (s *PostgresStore) upsertDoc(ctx context.Context, table, idCol, id string, v any, cols []sqlCol) error {
	table, idCol, err := checkedIdents(table, idCol)
	if err != nil {
		return err
	}
	if err = checkCols(cols); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	names := []string{idCol, "data", "revision"}
	args := []any{id, b, 1}
	sets := []string{"data = EXCLUDED.data", "revision = " + table + ".revision + 1"}
	for _, c := range cols {
		args = append(args, c.val)
		names = append(names, c.name)
		sets = append(sets, c.name+" = EXCLUDED."+c.name)
	}
	placeholders := make([]string, len(args))
	for i := range args {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	q := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO UPDATE SET %s`,
		table, strings.Join(names, ", "), strings.Join(placeholders, ", "), idCol, strings.Join(sets, ", "))
	_, err = s.db.ExecContext(ctx, q, args...)
	return err
}

func occDoc[T any](ctx context.Context, s *PostgresStore, table, idCol, id string, fn func(*T) []sqlCol) error {
	for attempt := 0; attempt < occRetries; attempt++ {
		raw, rev, ok, err := s.loadRev(ctx, table, idCol, id)
		if err != nil {
			return err
		}
		if !ok {
			return os.ErrNotExist
		}
		var v T
		if err = json.Unmarshal(raw, &v); err != nil {
			return err
		}
		cols := fn(&v)
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		n, err := s.updateRevision(ctx, table, idCol, id, rev, b, cols)
		if err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
	}
	return ErrConflict
}

func (s *PostgresStore) loadRev(ctx context.Context, table, idCol, id string) ([]byte, int64, bool, error) {
	table, idCol, err := checkedIdents(table, idCol)
	if err != nil {
		return nil, 0, false, err
	}
	var raw []byte
	var rev int64
	err = s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT data, revision FROM %s WHERE %s = $1`, table, idCol), id).Scan(&raw, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	return raw, rev, true, nil
}

func (s *PostgresStore) updateRevision(ctx context.Context, table, idCol, id string, rev int64, data []byte, cols []sqlCol) (int64, error) {
	table, idCol, err := checkedIdents(table, idCol)
	if err != nil {
		return 0, err
	}
	if err = checkCols(cols); err != nil {
		return 0, err
	}
	sets := []string{"data = $1", "revision = revision + 1"}
	args := []any{data}
	for _, c := range cols {
		args = append(args, c.val)
		sets = append(sets, fmt.Sprintf("%s = $%d", c.name, len(args)))
	}
	args = append(args, id, rev)
	q := fmt.Sprintf(`UPDATE %s SET %s WHERE %s = $%d AND revision = $%d`,
		table, strings.Join(sets, ", "), idCol, len(args)-1, len(args))
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return n, err
}

func (s *PostgresStore) deleteID(ctx context.Context, table, idCol, id string) error {
	table, idCol, err := checkedIdents(table, idCol)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s = $1`, table, idCol), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return os.ErrNotExist
	}
	return nil
}

func getOne[T any](ctx context.Context, db *sql.DB, query string, args ...any) (T, bool) {
	var zero T
	var raw []byte
	err := db.QueryRowContext(ctx, query, args...).Scan(&raw)
	if err != nil {
		return zero, false
	}
	var v T
	if json.Unmarshal(raw, &v) != nil {
		return zero, false
	}
	return v, true
}

func queryJSON[T any](ctx context.Context, q queryer, query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var v T
		if err = json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func checkedIdents(table, idCol string) (string, string, error) {
	if !identRe.MatchString(table) || !identRe.MatchString(idCol) {
		return "", "", fmt.Errorf("invalid sql identifier")
	}
	return table, idCol, nil
}

func checkCols(cols []sqlCol) error {
	for _, c := range cols {
		if !identRe.MatchString(c.name) {
			return fmt.Errorf("invalid sql identifier %q", c.name)
		}
	}
	return nil
}
