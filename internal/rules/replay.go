// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package rules

import (
	"encoding/json"
	"fmt"
	"time"
)

// Replay limits.
const (
	MaxReplayMessages = 10000
	MaxReplayReport   = 1000
)

// Message is one recorded sample. A zero Time means the previous message's
// time plus one second (the first defaults to the replay start).
type Message struct {
	Topic   string          `json:"topic"`
	Payload json.RawMessage `json:"payload"`
	Time    time.Time       `json:"time,omitempty"`
}

// Fired is one transition found by a replay together with the events the
// actions would ingest.
type Fired struct {
	Index      int       `json:"index"`
	At         time.Time `json:"at"`
	Rule       string    `json:"rule"`
	Key        string    `json:"key"`
	Kind       string    `json:"kind"`
	Value      float64   `json:"value"`
	Suppressed bool      `json:"suppressed,omitempty"`
	Events     []Event   `json:"events,omitempty"`
}

// Report is the result of a dry-run replay.
type Report struct {
	Messages    int      `json:"messages"`
	Fires       int      `json:"fires"`
	Clears      int      `json:"clears"`
	Suppressed  int      `json:"rate_limited"`
	Skipped     uint64   `json:"skipped"`
	OutOfWindow uint64   `json:"out_of_window"`
	KeyOverflow uint64   `json:"key_overflow"`
	Transitions []Fired  `json:"transitions"`
	Truncated   bool     `json:"truncated,omitempty"`
	FinalFiring []string `json:"final_firing"`
}

// Replay runs msgs through a fresh engine for rs and reports what would fire.
// Nothing is persisted and no action is executed.
func Replay(rs *RuleSet, start time.Time, msgs []Message) (Report, error) {
	if len(msgs) > MaxReplayMessages {
		return Report{}, fmt.Errorf("at most %d messages per replay", MaxReplayMessages)
	}
	e := NewEngine(rs)
	rep := Report{Transitions: []Fired{}}
	at := start
	for i, m := range msgs {
		if !m.Time.IsZero() {
			at = m.Time
		} else if i > 0 {
			at = at.Add(time.Second)
		}
		rep.Messages++
		for _, o := range e.Evaluate(at, m.Topic, m.Payload) {
			if o.Kind == "fire" {
				rep.Fires++
			} else {
				rep.Clears++
			}
			if o.Suppressed {
				rep.Suppressed++
			}
			if len(rep.Transitions) >= MaxReplayReport {
				rep.Truncated = true
				continue
			}
			f := Fired{Index: i, At: o.At, Rule: o.Rule, Key: o.Key, Kind: o.Kind, Value: o.Value, Suppressed: o.Suppressed}
			for _, a := range o.Actions {
				ev, err := Render(o, a)
				if err != nil {
					return rep, err
				}
				f.Events = append(f.Events, ev)
			}
			rep.Transitions = append(rep.Transitions, f)
		}
	}
	c := e.Counters()
	rep.Skipped, rep.OutOfWindow, rep.KeyOverflow = c.Skipped, c.OutOfWindow, c.KeyOverflow
	rep.FinalFiring = e.Firing()
	if rep.FinalFiring == nil {
		rep.FinalFiring = []string{}
	}
	return rep, nil
}
