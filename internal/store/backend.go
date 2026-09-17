// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"time"

	"github.com/zyvorai/nodra/internal/model"
)

// Backend is the control-plane fleet state persistence surface.
// The default file WAL implementation and optional Postgres backend both satisfy it.
type Backend interface {
	Snapshot() model.State
	Ping(ctx context.Context) error
	AddSite(v model.Site) error
	Site(id string) (model.Site, bool)
	UpdateSite(id string, fn func(*model.Site)) error
	Sites() []model.Site
	AddDevice(v model.Device) error
	Devices() []model.Device
	Device(id string) (model.Device, bool)
	Twin(deviceID string) (model.Twin, bool)
	TwinsForSite(siteID string) []model.Twin
	SetTwin(v model.Twin) error
	AddRoute(v model.Route) error
	Routes() []model.Route
	DeleteRoute(id string) error
	AddDeployment(v model.Deployment) error
	Deployments() []model.Deployment
	Deployment(id string) (model.Deployment, bool)
	UpdateDeployment(id string, fn func(*model.Deployment)) error
	DeleteDeployment(id string) error
	AddPolicyPack(v model.PolicyPack) error
	PolicyPacks() []model.PolicyPack
	PolicyPack(id string) (model.PolicyPack, bool)
	UpdatePolicyPack(id string, fn func(*model.PolicyPack)) error
	DeletePolicyPack(id string) error
	AddOTACampaign(v model.OTACampaign) error
	OTACampaigns() []model.OTACampaign
	OTACampaign(id string) (model.OTACampaign, bool)
	UpdateOTACampaign(id string, fn func(*model.OTACampaign)) error
	AddOrg(v model.Org) error
	Orgs() []model.Org
	Org(id string) (model.Org, bool)
	UpdateOrg(id string, fn func(*model.Org)) error
	DeleteOrg(id string) error
	AddAlert(v model.Alert) error
	Alerts() []model.Alert
	ResolveAlert(id string) error
	AddEvent(v model.Event) error
	HasEvent(id string) bool
	EventsSince(t time.Time) []model.Event
	Close() error
}

// OpenBackend opens the configured fleet store.
// driver: "file" (default) or "postgres".
func OpenBackend(driver, filePath, databaseURL string) (Backend, error) {
	switch driver {
	case "", "file":
		return Open(filePath)
	case "postgres":
		return OpenPostgres(databaseURL)
	default:
		return nil, errUnsupportedDriver(driver)
	}
}
