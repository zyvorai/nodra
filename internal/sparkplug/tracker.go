// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package sparkplug

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tracker limits keep hostile publishers from growing memory without bound.
const (
	MaxNodes          = 10000
	MaxDevicesPerNode = 1000
)

// Anomaly names reported by Observe.
const (
	AnomalySeqGap           = "seq_gap"
	AnomalyBirthSeq         = "birth_seq_nonzero"
	AnomalyStaleDeath       = "stale_ndeath"
	AnomalyNoNodeBirth      = "data_without_nbirth"
	AnomalyNoDeviceBirth    = "data_without_dbirth"
	AnomalyUnknownAlias     = "unknown_alias"
	AnomalyTrackerFull      = "tracker_full"
	AnomalyDeviceBeforeNode = "dbirth_without_nbirth"
	AnomalyMissingSeq       = "missing_seq"
)

// EntityState is the tracked liveness of an edge node or one of its devices.
type EntityState struct {
	Group        string    `json:"group"`
	Node         string    `json:"node"`
	Device       string    `json:"device,omitempty"`
	Online       bool      `json:"online"`
	BdSeq        uint64    `json:"bd_seq,omitempty"`
	LastSeq      int       `json:"last_seq"`
	NeedsRebirth bool      `json:"needs_rebirth,omitempty"`
	LastChange   time.Time `json:"last_change"`
	LastSeen     time.Time `json:"last_seen"`
}

type entity struct {
	EntityState
	aliases map[uint64]string
}

type nodeRec struct {
	self    entity
	devices map[string]*entity
}

// Observation is the result of feeding one message to the Tracker.
type Observation struct {
	Topic     Topic
	Payload   Payload
	Anomalies []string
	// Changed is true when the online/offline state of the node or device
	// flipped (or was first established) because of this message.
	Changed bool
	State   *EntityState // current state of the addressed entity after the message
}

// Tracker follows Sparkplug birth/death certificates and sequence numbers.
// It is safe for concurrent use.
type Tracker struct {
	mu    sync.Mutex
	nodes map[string]*nodeRec
	hosts map[string]bool
}

func NewTracker() *Tracker {
	return &Tracker{nodes: map[string]*nodeRec{}, hosts: map[string]bool{}}
}

func nodeKey(group, node string) string { return group + "\x00" + node }

// Observe applies a decoded message. Metric names are resolved from aliases
// recorded at birth, in place.
func (t *Tracker) Observe(tp Topic, p Payload, now time.Time) Observation {
	t.mu.Lock()
	defer t.mu.Unlock()
	obs := Observation{Topic: tp, Payload: p}
	if tp.Type == NCMD || tp.Type == DCMD || tp.Type == STATE {
		return obs
	}
	key := nodeKey(tp.Group, tp.Node)
	n := t.nodes[key]
	if n == nil {
		if tp.Type == NDEATH || tp.Type == DDEATH {
			// A death for something we never saw: nothing to track.
			return obs
		}
		if len(t.nodes) >= MaxNodes {
			obs.Anomalies = append(obs.Anomalies, AnomalyTrackerFull)
			return obs
		}
		n = &nodeRec{devices: map[string]*entity{}}
		n.self = entity{EntityState: EntityState{Group: tp.Group, Node: tp.Node, LastSeq: -1}}
		t.nodes[key] = n
	}
	n.self.LastSeen = now

	switch tp.Type {
	case NBIRTH:
		if !p.HasSeq || p.Seq != 0 {
			obs.Anomalies = append(obs.Anomalies, AnomalyBirthSeq)
		}
		obs.Changed = !n.self.Online
		n.self.Online, n.self.NeedsRebirth = true, false
		n.self.LastSeq = int(p.Seq & 0xff)
		if bd, ok := p.BdSeq(); ok {
			n.self.BdSeq = bd
		}
		n.self.aliases = learnAliases(p)
		resolve(&obs.Payload, n.self.aliases)
		// A new node session invalidates every device birth.
		for _, d := range n.devices {
			if d.Online {
				d.Online, d.LastChange = false, now
			}
		}
		n.self.LastChange = now
		obs.State = pub(&n.self)
	case NDEATH:
		bd, ok := p.BdSeq()
		if !ok || bd != n.self.BdSeq || !n.self.Online {
			// Stale will message from a previous session: the spec says to
			// ignore it, a newer NBIRTH already superseded that session.
			obs.Anomalies = append(obs.Anomalies, AnomalyStaleDeath)
			obs.State = pub(&n.self)
			return obs
		}
		n.self.Online = false
		n.self.LastChange = now
		obs.Changed = true
		for _, d := range n.devices {
			if d.Online {
				d.Online, d.LastChange = false, now
			}
		}
		obs.State = pub(&n.self)
	case DBIRTH, DDEATH, DDATA, NDATA:
		t.sequence(n, p, &obs, now)
		switch tp.Type {
		case NDATA:
			if !n.self.Online {
				obs.Anomalies = append(obs.Anomalies, AnomalyNoNodeBirth)
				n.self.NeedsRebirth = true
			}
			resolveChecked(&obs, n.self.aliases)
			obs.State = pub(&n.self)
			return obs
		}
		d := n.devices[tp.Device]
		if d == nil {
			if tp.Type == DDEATH {
				return obs
			}
			if len(n.devices) >= MaxDevicesPerNode {
				obs.Anomalies = append(obs.Anomalies, AnomalyTrackerFull)
				return obs
			}
			d = &entity{EntityState: EntityState{Group: tp.Group, Node: tp.Node, Device: tp.Device, LastSeq: -1}}
			n.devices[tp.Device] = d
		}
		d.LastSeen = now
		switch tp.Type {
		case DBIRTH:
			if !n.self.Online {
				obs.Anomalies = append(obs.Anomalies, AnomalyDeviceBeforeNode)
				n.self.NeedsRebirth = true
			}
			obs.Changed = !d.Online
			d.Online, d.NeedsRebirth = true, false
			d.aliases = learnAliases(p)
			resolve(&obs.Payload, d.aliases)
			d.LastChange = now
		case DDEATH:
			obs.Changed = d.Online
			d.Online = false
			d.LastChange = now
		case DDATA:
			if !d.Online {
				obs.Anomalies = append(obs.Anomalies, AnomalyNoDeviceBirth)
				d.NeedsRebirth = true
			}
			resolveChecked(&obs, d.aliases)
		}
		obs.State = pub(d)
	}
	return obs
}

// sequence validates the node-wide 0..255 sequence counter.
func (t *Tracker) sequence(n *nodeRec, p Payload, obs *Observation, _ time.Time) {
	if !p.HasSeq {
		obs.Anomalies = append(obs.Anomalies, AnomalyMissingSeq)
		return
	}
	if n.self.LastSeq >= 0 && int(p.Seq) != (n.self.LastSeq+1)%256 {
		obs.Anomalies = append(obs.Anomalies, AnomalySeqGap)
		n.self.NeedsRebirth = true
	}
	n.self.LastSeq = int(p.Seq & 0xff)
}

func learnAliases(p Payload) map[uint64]string {
	m := map[uint64]string{}
	for _, mt := range p.Metrics {
		if mt.HasAlias && mt.Name != "" {
			m[mt.Alias] = mt.Name
		}
	}
	return m
}

func resolve(p *Payload, aliases map[uint64]string) {
	for i := range p.Metrics {
		if p.Metrics[i].Name == "" && p.Metrics[i].HasAlias {
			p.Metrics[i].Name = aliases[p.Metrics[i].Alias]
		}
	}
}

func resolveChecked(obs *Observation, aliases map[uint64]string) {
	for i := range obs.Payload.Metrics {
		m := &obs.Payload.Metrics[i]
		if m.Name == "" && m.HasAlias {
			if name, ok := aliases[m.Alias]; ok {
				m.Name = name
			} else {
				obs.Anomalies = append(obs.Anomalies, AnomalyUnknownAlias)
			}
		}
	}
}

func pub(e *entity) *EntityState { s := e.EntityState; return &s }

// SetHost records a STATE message from a Sparkplug primary host application.
func (t *Tracker) SetHost(host string, online bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.hosts[host]; !ok && len(t.hosts) >= 256 {
		return
	}
	t.hosts[host] = online
}

// ParseHostState reads a STATE payload: JSON {"online":bool} or the legacy
// plain strings ONLINE/OFFLINE.
func ParseHostState(b []byte) (online, ok bool) {
	s := strings.TrimSpace(string(b))
	switch strings.ToUpper(s) {
	case "ONLINE":
		return true, true
	case "OFFLINE":
		return false, true
	}
	var v struct {
		Online *bool `json:"online"`
	}
	if json.Unmarshal(b, &v) == nil && v.Online != nil {
		return *v.Online, true
	}
	return false, false
}

// Snapshot returns every tracked node and device, sorted for stable output.
func (t *Tracker) Snapshot() []EntityState {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []EntityState
	for _, n := range t.nodes {
		out = append(out, n.self.EntityState)
		for _, d := range n.devices {
			out = append(out, d.EntityState)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		return a.Device < b.Device
	})
	return out
}

// Hosts returns the last seen primary-host states.
func (t *Tracker) Hosts() map[string]bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]bool, len(t.hosts))
	for k, v := range t.hosts {
		out[k] = v
	}
	return out
}

// Envelope is the JSON form ingested for a decoded Sparkplug message.
type Envelope struct {
	Namespace string       `json:"namespace"`
	Group     string       `json:"group,omitempty"`
	Type      string       `json:"type"`
	Node      string       `json:"node,omitempty"`
	Device    string       `json:"device,omitempty"`
	Seq       uint64       `json:"seq"`
	Timestamp uint64       `json:"timestamp,omitempty"`
	Metrics   []Metric     `json:"metrics"`
	Anomalies []string     `json:"anomalies,omitempty"`
	State     *EntityState `json:"state,omitempty"`
}

// JSON renders the observation as an ingestible JSON document.
func (o Observation) JSON() ([]byte, error) {
	ms := o.Payload.Metrics
	if ms == nil {
		ms = []Metric{}
	}
	return json.Marshal(Envelope{Namespace: Namespace, Group: o.Topic.Group, Type: o.Topic.Type, Node: o.Topic.Node, Device: o.Topic.Device,
		Seq: o.Payload.Seq, Timestamp: o.Payload.Timestamp, Metrics: ms, Anomalies: o.Anomalies, State: o.State})
}
