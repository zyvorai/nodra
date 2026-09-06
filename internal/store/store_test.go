// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"github.com/zyvorai/nodra/internal/model"
	"path/filepath"
	"testing"
	"time"
)

func TestWALPersistence(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	s, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AddSite(model.Site{ID: "s1", Name: "x", CreatedAt: time.Now()}); e != nil {
		t.Fatal(e)
	}
	if e = s.AddEvent(model.Event{ID: "e1", SiteID: "s1", Topic: "x", EventTime: time.Now()}); e != nil {
		t.Fatal(e)
	}
	_ = s.Close()
	s2, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer s2.Close()
	if _, ok := s2.Site("s1"); !ok {
		t.Fatal("site missing")
	}
	if !s2.HasEvent("e1") {
		t.Fatal("event missing")
	}
}
func TestTwinPersistence(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	defer s.Close()
	_ = s.SetTwin(model.Twin{DeviceID: "d1", SiteID: "s1", Desired: map[string]any{"mode": "auto"}, DesiredVersion: 1})
	tw, ok := s.Twin("d1")
	if !ok || tw.DesiredVersion != 1 {
		t.Fatalf("%+v %v", tw, ok)
	}
}
