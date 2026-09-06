package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zyvorai/nodra/internal/durable"
	"github.com/zyvorai/nodra/internal/model"
)

type op struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
	ID   string          `json:"id,omitempty"`
}

type Store struct {
	mu           sync.RWMutex
	dir          string
	snapshotPath string
	walPath      string
	wal          *os.File
	state        model.State
	seen         map[string]struct{}
	ops          int
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store path is required")
	}
	dir := filepath.Dir(path)
	// v0.2 treats the old state.json path as a directory anchor for compatibility.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, snapshotPath: filepath.Join(dir, "state.snapshot.json"), walPath: filepath.Join(dir, "state.wal"), seen: map[string]struct{}{}}
	// Migrate v0.1 state.json once if present and no snapshot exists.
	if _, err := os.Stat(s.snapshotPath); os.IsNotExist(err) {
		if b, er := os.ReadFile(path); er == nil && len(b) > 0 {
			if er = json.Unmarshal(b, &s.state); er != nil {
				return nil, er
			}
		}
	}
	if b, err := os.ReadFile(s.snapshotPath); err == nil && len(b) > 0 {
		if err = json.Unmarshal(b, &s.state); err != nil {
			return nil, err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, id := range s.state.SeenEvents {
		s.seen[id] = struct{}{}
	}
	if err := s.replay(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	s.wal = f
	if _, err := os.Stat(path); err == nil && path != s.snapshotPath {
		_ = os.Rename(path, path+".v0.1.migrated")
	}
	return s, nil
}

func (s *Store) replay() error {
	f, err := os.Open(s.walPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var o op
		if err = json.Unmarshal(sc.Bytes(), &o); err != nil {
			return fmt.Errorf("replay state wal: %w", err)
		}
		if err = s.apply(o); err != nil {
			return err
		}
	}
	return sc.Err()
}
func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func (s *Store) appendLocked(o op) error {
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err = s.wal.Write(b); err != nil {
		return err
	}
	if err = s.wal.Sync(); err != nil {
		return err
	}
	s.ops++
	if s.ops >= 2048 {
		return s.compactLocked()
	}
	return nil
}
func (s *Store) compactLocked() error {
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	if err = durable.AtomicWrite(s.snapshotPath, b, 0o600); err != nil {
		return err
	}
	if err = s.wal.Close(); err != nil {
		return err
	}
	if err = durable.AtomicWrite(s.walPath, nil, 0o600); err != nil {
		return err
	}
	s.wal, err = os.OpenFile(s.walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	s.ops = 0
	return err
}
func (s *Store) mutate(o op) error {
	if err := s.appendLocked(o); err != nil {
		return err
	}
	return s.apply(o)
}
func (s *Store) apply(o op) error {
	switch o.Type {
	case "site.add":
		var v model.Site
		_ = json.Unmarshal(o.Data, &v)
		s.state.Sites = append(s.state.Sites, v)
	case "site.set":
		var v model.Site
		_ = json.Unmarshal(o.Data, &v)
		for i := range s.state.Sites {
			if s.state.Sites[i].ID == v.ID {
				s.state.Sites[i] = v
				return nil
			}
		}
		s.state.Sites = append(s.state.Sites, v)
	case "device.add":
		var v model.Device
		_ = json.Unmarshal(o.Data, &v)
		for i := range s.state.Devices {
			if s.state.Devices[i].ID == v.ID {
				s.state.Devices[i] = v
				return nil
			}
		}
		s.state.Devices = append(s.state.Devices, v)
	case "twin.set":
		var v model.Twin
		_ = json.Unmarshal(o.Data, &v)
		for i := range s.state.Twins {
			if s.state.Twins[i].DeviceID == v.DeviceID {
				s.state.Twins[i] = v
				return nil
			}
		}
		s.state.Twins = append(s.state.Twins, v)
	case "route.add":
		var v model.Route
		_ = json.Unmarshal(o.Data, &v)
		s.state.Routes = append(s.state.Routes, v)
	case "route.del":
		out := s.state.Routes[:0]
		for _, v := range s.state.Routes {
			if v.ID != o.ID {
				out = append(out, v)
			}
		}
		s.state.Routes = out
	case "deployment.add":
		var v model.Deployment
		_ = json.Unmarshal(o.Data, &v)
		s.state.Deployments = append(s.state.Deployments, v)
	case "deployment.set":
		var v model.Deployment
		_ = json.Unmarshal(o.Data, &v)
		for i := range s.state.Deployments {
			if s.state.Deployments[i].ID == v.ID {
				s.state.Deployments[i] = v
				return nil
			}
		}
	case "deployment.del":
		out := s.state.Deployments[:0]
		for _, v := range s.state.Deployments {
			if v.ID != o.ID {
				out = append(out, v)
			}
		}
		s.state.Deployments = out
	case "alert.add":
		var v model.Alert
		_ = json.Unmarshal(o.Data, &v)
		s.state.Alerts = append(s.state.Alerts, v)
		if len(s.state.Alerts) > 2000 {
			s.state.Alerts = s.state.Alerts[len(s.state.Alerts)-2000:]
		}
	case "alert.set":
		var v model.Alert
		_ = json.Unmarshal(o.Data, &v)
		for i := range s.state.Alerts {
			if s.state.Alerts[i].ID == v.ID {
				s.state.Alerts[i] = v
				return nil
			}
		}
	case "event.add":
		var v model.Event
		_ = json.Unmarshal(o.Data, &v)
		s.state.Events = append(s.state.Events, v)
		s.state.SeenEvents = append(s.state.SeenEvents, v.ID)
		s.seen[v.ID] = struct{}{}
		if len(s.state.Events) > 2000 {
			s.state.Events = s.state.Events[len(s.state.Events)-2000:]
		}
		if len(s.state.SeenEvents) > 20000 {
			for _, id := range s.state.SeenEvents[:len(s.state.SeenEvents)-20000] {
				delete(s.seen, id)
			}
			s.state.SeenEvents = s.state.SeenEvents[len(s.state.SeenEvents)-20000:]
		}
	default:
		return fmt.Errorf("unknown state operation %q", o.Type)
	}
	return nil
}

func (s *Store) Snapshot() model.State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, _ := json.Marshal(s.state)
	var out model.State
	_ = json.Unmarshal(b, &out)
	return out
}
func (s *Store) AddSite(v model.Site) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(op{Type: "site.add", Data: mustJSON(v)})
}
func (s *Store) Site(id string) (model.Site, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.state.Sites {
		if v.ID == id {
			return v, true
		}
	}
	return model.Site{}, false
}
func (s *Store) UpdateSite(id string, fn func(*model.Site)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.state.Sites {
		if v.ID == id {
			fn(&v)
			return s.mutate(op{Type: "site.set", Data: mustJSON(v)})
		}
	}
	return os.ErrNotExist
}
func (s *Store) Sites() []model.Site { return s.Snapshot().Sites }
func (s *Store) AddDevice(v model.Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(op{Type: "device.add", Data: mustJSON(v)})
}
func (s *Store) Devices() []model.Device { return s.Snapshot().Devices }
func (s *Store) Device(id string) (model.Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.state.Devices {
		if v.ID == id {
			return v, true
		}
	}
	return model.Device{}, false
}
func (s *Store) Twin(deviceID string) (model.Twin, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.state.Twins {
		if v.DeviceID == deviceID {
			return v, true
		}
	}
	return model.Twin{}, false
}
func (s *Store) TwinsForSite(siteID string) []model.Twin {
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
func (s *Store) SetTwin(v model.Twin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(op{Type: "twin.set", Data: mustJSON(v)})
}
func (s *Store) AddRoute(v model.Route) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(op{Type: "route.add", Data: mustJSON(v)})
}
func (s *Store) Routes() []model.Route { return s.Snapshot().Routes }
func (s *Store) DeleteRoute(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, v := range s.state.Routes {
		if v.ID == id {
			found = true
			break
		}
	}
	if !found {
		return os.ErrNotExist
	}
	return s.mutate(op{Type: "route.del", ID: id})
}
func (s *Store) AddDeployment(v model.Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(op{Type: "deployment.add", Data: mustJSON(v)})
}
func (s *Store) Deployments() []model.Deployment { return s.Snapshot().Deployments }
func (s *Store) Deployment(id string) (model.Deployment, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.state.Deployments {
		if v.ID == id {
			return v, true
		}
	}
	return model.Deployment{}, false
}
func (s *Store) UpdateDeployment(id string, fn func(*model.Deployment)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.state.Deployments {
		if v.ID == id {
			fn(&v)
			return s.mutate(op{Type: "deployment.set", Data: mustJSON(v)})
		}
	}
	return os.ErrNotExist
}
func (s *Store) DeleteDeployment(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, v := range s.state.Deployments {
		if v.ID == id {
			found = true
			break
		}
	}
	if !found {
		return os.ErrNotExist
	}
	return s.mutate(op{Type: "deployment.del", ID: id})
}
func (s *Store) AddAlert(v model.Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(op{Type: "alert.add", Data: mustJSON(v)})
}
func (s *Store) Alerts() []model.Alert { return s.Snapshot().Alerts }
func (s *Store) ResolveAlert(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.state.Alerts {
		if v.ID == id {
			v.Resolved = true
			return s.mutate(op{Type: "alert.set", Data: mustJSON(v)})
		}
	}
	return os.ErrNotExist
}
func (s *Store) AddEvent(v model.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[v.ID]; ok {
		return nil
	}
	return s.mutate(op{Type: "event.add", Data: mustJSON(v)})
}
func (s *Store) HasEvent(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.seen[id]
	return ok
}
func (s *Store) EventsSince(t time.Time) []model.Event {
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
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wal != nil {
		return s.wal.Close()
	}
	return nil
}
