// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/zyvorai/nodra/internal/model"
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

func TestFileStorePing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Ping(context.Background()); err != nil {
		t.Fatal(err)
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

func TestOTACampaignPersistence(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	camp := model.OTACampaign{
		ID: "otacamp_1", Name: "rollout", Status: model.OTACampaignDraft,
		SiteIDs: []string{"s1"}, CurrentStage: -1,
		Stages: []model.OTACampaignStage{{CanaryPercent: 100}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.AddOTACampaign(camp); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateOTACampaign(camp.ID, func(c *model.OTACampaign) {
		c.Status = model.OTACampaignRunning
		c.CurrentStage = 0
	}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, ok := s2.OTACampaign(camp.ID)
	if !ok {
		t.Fatal("campaign missing after reopen")
	}
	if got.Status != model.OTACampaignRunning || got.CurrentStage != 0 || got.Name != "rollout" {
		t.Fatalf("unexpected campaign after reopen: %+v", got)
	}
	if len(s2.OTACampaigns()) != 1 {
		t.Fatalf("want 1 campaign, got %d", len(s2.OTACampaigns()))
	}
}
