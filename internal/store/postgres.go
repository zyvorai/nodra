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
	"sync"
	"time"

	"github.com/zyvorai/nodra/internal/model"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresStore persists fleet state in PostgreSQL (write-through + in-memory index).
// Delivery/DLQ queues remain local WAL; this backend is for control-plane fleet metadata.
type PostgresStore struct {
	mu    sync.RWMutex
	db    *sql.DB
	state model.State
	seen  map[string]struct{}
}

const pgSchema = `
CREATE TABLE IF NOT EXISTS nodra_sites (id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_devices (id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_twins (device_id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_routes (id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_deployments (id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_alerts (id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_events (id TEXT PRIMARY KEY, data JSONB NOT NULL, event_time TIMESTAMPTZ);
CREATE TABLE IF NOT EXISTS nodra_seen_events (id TEXT PRIMARY KEY);
`

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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	if _, err = db.ExecContext(ctx, pgSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres migrate: %w", err)
	}
	s := &PostgresStore{db: db, seen: map[string]struct{}{}}
	if err = s.load(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *PostgresStore) load() error {
	ctx := context.Background()
	loadJSON := func(query string, add func([]byte) error) error {
		rows, err := s.db.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b []byte
			if err = rows.Scan(&b); err != nil {
				return err
			}
			if err = add(b); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	if err := loadJSON(`SELECT data FROM nodra_sites`, func(b []byte) error {
		var v model.Site
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		s.state.Sites = append(s.state.Sites, v)
		return nil
	}); err != nil {
		return err
	}
	if err := loadJSON(`SELECT data FROM nodra_devices`, func(b []byte) error {
		var v model.Device
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		s.state.Devices = append(s.state.Devices, v)
		return nil
	}); err != nil {
		return err
	}
	if err := loadJSON(`SELECT data FROM nodra_twins`, func(b []byte) error {
		var v model.Twin
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		s.state.Twins = append(s.state.Twins, v)
		return nil
	}); err != nil {
		return err
	}
	if err := loadJSON(`SELECT data FROM nodra_routes`, func(b []byte) error {
		var v model.Route
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		s.state.Routes = append(s.state.Routes, v)
		return nil
	}); err != nil {
		return err
	}
	if err := loadJSON(`SELECT data FROM nodra_deployments`, func(b []byte) error {
		var v model.Deployment
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		s.state.Deployments = append(s.state.Deployments, v)
		return nil
	}); err != nil {
		return err
	}
	if err := loadJSON(`SELECT data FROM nodra_alerts`, func(b []byte) error {
		var v model.Alert
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		s.state.Alerts = append(s.state.Alerts, v)
		return nil
	}); err != nil {
		return err
	}
	if err := loadJSON(`SELECT data FROM nodra_events`, func(b []byte) error {
		var v model.Event
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		s.state.Events = append(s.state.Events, v)
		return nil
	}); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM nodra_seen_events`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return err
		}
		s.seen[id] = struct{}{}
		s.state.SeenEvents = append(s.state.SeenEvents, id)
	}
	return rows.Err()
}

func (s *PostgresStore) Snapshot() model.State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, _ := json.Marshal(s.state)
	var out model.State
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *PostgresStore) upsert(table, idCol, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	q := fmt.Sprintf(`INSERT INTO %s (%s, data) VALUES ($1, $2) ON CONFLICT (%s) DO UPDATE SET data = EXCLUDED.data`, table, idCol, idCol)
	_, err = s.db.Exec(q, id, b)
	return err
}

func (s *PostgresStore) AddSite(v model.Site) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.upsert("nodra_sites", "id", v.ID, v); err != nil {
		return err
	}
	s.state.Sites = append(s.state.Sites, v)
	return nil
}
func (s *PostgresStore) Site(id string) (model.Site, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.state.Sites {
		if v.ID == id {
			return v, true
		}
	}
	return model.Site{}, false
}
func (s *PostgresStore) UpdateSite(id string, fn func(*model.Site)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, v := range s.state.Sites {
		if v.ID == id {
			fn(&v)
			if err := s.upsert("nodra_sites", "id", v.ID, v); err != nil {
				return err
			}
			s.state.Sites[i] = v
			return nil
		}
	}
	return os.ErrNotExist
}
func (s *PostgresStore) Sites() []model.Site { return s.Snapshot().Sites }

func (s *PostgresStore) AddDevice(v model.Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.upsert("nodra_devices", "id", v.ID, v); err != nil {
		return err
	}
	for i := range s.state.Devices {
		if s.state.Devices[i].ID == v.ID {
			s.state.Devices[i] = v
			return nil
		}
	}
	s.state.Devices = append(s.state.Devices, v)
	return nil
}
func (s *PostgresStore) Devices() []model.Device { return s.Snapshot().Devices }
func (s *PostgresStore) Device(id string) (model.Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.state.Devices {
		if v.ID == id {
			return v, true
		}
	}
	return model.Device{}, false
}

func (s *PostgresStore) Twin(deviceID string) (model.Twin, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.state.Twins {
		if v.DeviceID == deviceID {
			return v, true
		}
	}
	return model.Twin{}, false
}
func (s *PostgresStore) TwinsForSite(siteID string) []model.Twin {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []model.Twin
	for _, v := range s.state.Twins {
		if v.SiteID == siteID {
			out = append(out, v)
		}
	}
	return out
}
func (s *PostgresStore) SetTwin(v model.Twin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.upsert("nodra_twins", "device_id", v.DeviceID, v); err != nil {
		return err
	}
	for i := range s.state.Twins {
		if s.state.Twins[i].DeviceID == v.DeviceID {
			s.state.Twins[i] = v
			return nil
		}
	}
	s.state.Twins = append(s.state.Twins, v)
	return nil
}

func (s *PostgresStore) AddRoute(v model.Route) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.upsert("nodra_routes", "id", v.ID, v); err != nil {
		return err
	}
	s.state.Routes = append(s.state.Routes, v)
	return nil
}
func (s *PostgresStore) Routes() []model.Route { return s.Snapshot().Routes }
func (s *PostgresStore) DeleteRoute(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM nodra_routes WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return os.ErrNotExist
	}
	out := s.state.Routes[:0]
	for _, v := range s.state.Routes {
		if v.ID != id {
			out = append(out, v)
		}
	}
	s.state.Routes = out
	return nil
}

func (s *PostgresStore) AddDeployment(v model.Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.upsert("nodra_deployments", "id", v.ID, v); err != nil {
		return err
	}
	s.state.Deployments = append(s.state.Deployments, v)
	return nil
}
func (s *PostgresStore) Deployments() []model.Deployment { return s.Snapshot().Deployments }
func (s *PostgresStore) Deployment(id string) (model.Deployment, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.state.Deployments {
		if v.ID == id {
			return v, true
		}
	}
	return model.Deployment{}, false
}
func (s *PostgresStore) UpdateDeployment(id string, fn func(*model.Deployment)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, v := range s.state.Deployments {
		if v.ID == id {
			fn(&v)
			if err := s.upsert("nodra_deployments", "id", v.ID, v); err != nil {
				return err
			}
			s.state.Deployments[i] = v
			return nil
		}
	}
	return os.ErrNotExist
}
func (s *PostgresStore) DeleteDeployment(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM nodra_deployments WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return os.ErrNotExist
	}
	out := s.state.Deployments[:0]
	for _, v := range s.state.Deployments {
		if v.ID != id {
			out = append(out, v)
		}
	}
	s.state.Deployments = out
	return nil
}

func (s *PostgresStore) AddAlert(v model.Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.upsert("nodra_alerts", "id", v.ID, v); err != nil {
		return err
	}
	s.state.Alerts = append(s.state.Alerts, v)
	if len(s.state.Alerts) > 2000 {
		s.state.Alerts = s.state.Alerts[len(s.state.Alerts)-2000:]
	}
	return nil
}
func (s *PostgresStore) Alerts() []model.Alert { return s.Snapshot().Alerts }
func (s *PostgresStore) ResolveAlert(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, v := range s.state.Alerts {
		if v.ID == id {
			v.Resolved = true
			if err := s.upsert("nodra_alerts", "id", v.ID, v); err != nil {
				return err
			}
			s.state.Alerts[i] = v
			return nil
		}
	}
	return os.ErrNotExist
}

func (s *PostgresStore) AddEvent(v model.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[v.ID]; ok {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	when := v.EventTime
	if when.IsZero() {
		when = v.CreatedAt
	}
	if _, err = tx.Exec(`INSERT INTO nodra_events (id, data, event_time) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`, v.ID, b, when); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO nodra_seen_events (id) VALUES ($1) ON CONFLICT DO NOTHING`, v.ID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.state.Events = append(s.state.Events, v)
	s.state.SeenEvents = append(s.state.SeenEvents, v.ID)
	s.seen[v.ID] = struct{}{}
	if len(s.state.Events) > 2000 {
		s.state.Events = s.state.Events[len(s.state.Events)-2000:]
	}
	return nil
}
func (s *PostgresStore) HasEvent(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.seen[id]
	return ok
}
func (s *PostgresStore) EventsSince(t time.Time) []model.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []model.Event
	for _, e := range s.state.Events {
		when := e.EventTime
		if when.IsZero() {
			when = e.CreatedAt
		}
		if when.After(t) {
			out = append(out, e)
		}
	}
	return out
}

func (s *PostgresStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}
