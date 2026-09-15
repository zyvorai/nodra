// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"testing"

	"github.com/zyvorai/nodra/internal/model"
)

func TestMatchImageCrossesSlash(t *testing.T) {
	if !MatchImage("ghcr.io/zyvorai/*", "ghcr.io/zyvorai/foo/bar:v1") {
		t.Fatal("expected * to match across /")
	}
	if MatchImage("ghcr.io/zyvorai/*", "ghcr.io/other/bar:v1") {
		t.Fatal("expected non-matching prefix to be rejected")
	}
	if !MatchImage("*", "anything:latest") {
		t.Fatal("bare * should match anything")
	}
}

func TestAllowedNoPacksIsPermissive(t *testing.T) {
	ok, denied := Allowed(nil, "site-1", "docker.io/library/nginx:latest")
	if !ok || denied != "" {
		t.Fatalf("ok=%v denied=%q", ok, denied)
	}
}

func TestAllowedPackWithNoAllowedImagesIsNoop(t *testing.T) {
	packs := []model.PolicyPack{{ID: "p1", Name: "empty", Enabled: true}}
	ok, _ := Allowed(packs, "site-1", "anything:latest")
	if !ok {
		t.Fatal("pack with no AllowedImages should not constrain")
	}
}

func TestAllowedDisabledPackIgnored(t *testing.T) {
	packs := []model.PolicyPack{{ID: "p1", Name: "disabled", Enabled: false, AllowedImages: []string{"ghcr.io/zyvorai/*"}}}
	ok, _ := Allowed(packs, "site-1", "docker.io/evil/image:latest")
	if !ok {
		t.Fatal("disabled pack must not be enforced")
	}
}

func TestAllowedFleetWideDeniesNonMatching(t *testing.T) {
	packs := []model.PolicyPack{{ID: "p1", Name: "fleet", Enabled: true, AllowedImages: []string{"ghcr.io/zyvorai/*"}}}
	ok, denied := Allowed(packs, "site-1", "docker.io/evil/image:latest")
	if ok || denied != "fleet" {
		t.Fatalf("ok=%v denied=%q", ok, denied)
	}
	ok, _ = Allowed(packs, "site-1", "ghcr.io/zyvorai/nodra:v1")
	if !ok {
		t.Fatal("matching image should be allowed")
	}
}

func TestAllowedSiteScopedPackDoesNotAffectOtherSites(t *testing.T) {
	packs := []model.PolicyPack{{ID: "p1", Name: "site-only", Enabled: true, SiteID: "site-1", AllowedImages: []string{"ghcr.io/zyvorai/*"}}}
	ok, _ := Allowed(packs, "site-2", "docker.io/evil/image:latest")
	if !ok {
		t.Fatal("site-scoped pack must not constrain a different site")
	}
	ok, denied := Allowed(packs, "site-1", "docker.io/evil/image:latest")
	if ok || denied != "site-only" {
		t.Fatalf("ok=%v denied=%q", ok, denied)
	}
}

func TestAllowedMultiplePacksAreANDed(t *testing.T) {
	packs := []model.PolicyPack{
		{ID: "p1", Name: "fleet", Enabled: true, AllowedImages: []string{"ghcr.io/zyvorai/*"}},
		{ID: "p2", Name: "site", Enabled: true, SiteID: "site-1", AllowedImages: []string{"ghcr.io/zyvorai/nodra:*"}},
	}
	ok, _ := Allowed(packs, "site-1", "ghcr.io/zyvorai/nodra:v1")
	if !ok {
		t.Fatal("image satisfying both packs should be allowed")
	}
	ok, denied := Allowed(packs, "site-1", "ghcr.io/zyvorai/other:v1")
	if ok || denied != "site" {
		t.Fatalf("image satisfying only the fleet-wide pack should be denied by the site pack: ok=%v denied=%q", ok, denied)
	}
}
