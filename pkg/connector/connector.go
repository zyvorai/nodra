// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package connector defines the stable adapter surface for Nodra device/data connectors.
// Connectors translate protocol-specific data into Nodra events without coupling protocol
// implementations to the core runtime.
package connector

import "context"

type Event struct {
	Topic   string
	Payload []byte
	Headers map[string]string
}

type Health struct {
	Healthy bool
	Message string
}
type Handler func(context.Context, Event) error

type Connector interface {
	Name() string
	Start(context.Context, Handler) error
	Health(context.Context) Health
	Close() error
}
