// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package router

import "strings"

// Match implements MQTT-style topic filters: + matches one level, # matches the remaining levels.
func Match(filter, topic string) bool {
	f := strings.Split(strings.Trim(filter, "/"), "/")
	t := strings.Split(strings.Trim(topic, "/"), "/")
	for i := 0; i < len(f); i++ {
		if f[i] == "#" {
			return i == len(f)-1
		}
		if i >= len(t) {
			return false
		}
		if f[i] != "+" && f[i] != t[i] {
			return false
		}
	}
	return len(f) == len(t)
}
