// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package policy enforces fleet policy packs: v1 constrains only which
// deployment images are allowed (allowed_images), not RBAC rules or alert
// thresholds.
package policy

import (
	"regexp"
	"strings"

	"github.com/zyvorai/nodra/internal/model"
)

// MatchImage reports whether image matches pattern, where "*" matches any
// sequence of characters including "/". This is deliberately not
// path/filepath.Match's slash-stopping semantics: OCI image refs use "/" as
// an ordinary separator (registry/namespace/repo), not a path boundary a
// wildcard should stop at.
func MatchImage(pattern, image string) bool {
	parts := strings.Split(pattern, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	re := "^" + strings.Join(parts, ".*") + "$"
	matched, err := regexp.MatchString(re, image)
	return err == nil && matched
}

// Allowed reports whether image is permitted for siteID under every enabled,
// applicable policy pack (fleet-wide packs, i.e. SiteID=="", plus any pack
// scoped to siteID). A pack with no AllowedImages entries doesn't constrain
// anything. Among packs that do constrain, the image must match at least one
// pattern in EVERY such pack (constraints are ANDed across packs). When
// denied, deniedBy names the first pack that rejected the image.
func Allowed(packs []model.PolicyPack, siteID, image string) (ok bool, deniedBy string) {
	for _, p := range packs {
		if !p.Enabled {
			continue
		}
		if p.SiteID != "" && p.SiteID != siteID {
			continue
		}
		if len(p.AllowedImages) == 0 {
			continue
		}
		matched := false
		for _, pattern := range p.AllowedImages {
			if MatchImage(pattern, image) {
				matched = true
				break
			}
		}
		if !matched {
			return false, p.Name
		}
	}
	return true, ""
}
