// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

var ErrFull = errors.New("durable queue is full")

type Options struct {
	MaxItems int    `json:"max_items"`
	MaxBytes int64  `json:"max_bytes"`
	Policy   string `json:"policy"` // reject|drop-oldest|drop-newest
}

type Stats struct {
	Items    int    `json:"items"`
	Bytes    int64  `json:"bytes"`
	MaxItems int    `json:"max_items"`
	MaxBytes int64  `json:"max_bytes"`
	Policy   string `json:"policy"`
}

type record struct {
	Op   string          `json:"op"`
	ID   string          `json:"id"`
	Seq  uint64          `json:"seq"`
	Data json.RawMessage `json:"data,omitempty"`
}

type entry[T any] struct {
	ID    string
	Seq   uint64
	Data  T
	Bytes int64
}

type Queue[T any] struct {
	mu      sync.Mutex
	dir     string
	walPath string
	file    *os.File
	items   map[string]entry[T]
	seq     uint64
	bytes   int64
	ops     int
	opts    Options
}

func Open[T any](dir string) (*Queue[T], error) { return OpenWithOptions[T](dir, Options{}) }

func OpenWithOptions[T any](dir string, opts Options) (*Queue[T], error) {
	if dir == "" {
		return nil, errors.New("queue dir is required")
	}
	if opts.Policy == "" {
		opts.Policy = "reject"
	}
	switch opts.Policy {
	case "reject", "drop-oldest", "drop-newest":
	default:
		return nil, fmt.Errorf("invalid queue policy %q", opts.Policy)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	q := &Queue[T]{dir: dir, walPath: filepath.Join(dir, "queue.wal"), items: make(map[string]entry[T]), opts: opts}
	if err := q.replay(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(q.walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	q.file = f
	return q, nil
}

func (q *Queue[T]) replay() error {
	f, err := os.Open(q.walPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// Allow reasonably large edge records without an arbitrary 64 KiB scanner limit.
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return fmt.Errorf("replay queue wal: %w", err)
		}
		if r.Seq > q.seq {
			q.seq = r.Seq
		}
		switch r.Op {
		case "put":
			var v T
			if err := json.Unmarshal(r.Data, &v); err != nil {
				return fmt.Errorf("replay value: %w", err)
			}
			if old, ok := q.items[r.ID]; ok {
				q.bytes -= old.Bytes
			}
			q.items[r.ID] = entry[T]{ID: r.ID, Seq: r.Seq, Data: v, Bytes: int64(len(r.Data))}
			q.bytes += int64(len(r.Data))
		case "del":
			if old, ok := q.items[r.ID]; ok {
				q.bytes -= old.Bytes
				delete(q.items, r.ID)
			}
		}
	}
	return sc.Err()
}

func (q *Queue[T]) appendLocked(r record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err = q.file.Write(b); err != nil {
		return err
	}
	if err = q.file.Sync(); err != nil {
		return err
	}
	q.ops++
	if q.ops >= 4096 {
		return q.compactLocked()
	}
	return nil
}

func (q *Queue[T]) overLimit(addItems int, addBytes int64) bool {
	if q.opts.MaxItems > 0 && len(q.items)+addItems > q.opts.MaxItems {
		return true
	}
	if q.opts.MaxBytes > 0 && q.bytes+addBytes > q.opts.MaxBytes {
		return true
	}
	return false
}

func (q *Queue[T]) oldestIDLocked() string {
	var id string
	var seq uint64
	for k, e := range q.items {
		if id == "" || e.Seq < seq {
			id = k
			seq = e.Seq
		}
	}
	return id
}

func (q *Queue[T]) Put(id string, v T) error {
	if id == "" {
		return errors.New("queue id is required")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	old, exists := q.items[id]
	addItems := 1
	addBytes := int64(len(data))
	if exists {
		addItems = 0
		addBytes -= old.Bytes
	}
	if q.overLimit(addItems, addBytes) {
		switch q.opts.Policy {
		case "drop-newest":
			return ErrFull
		case "drop-oldest":
			for q.overLimit(addItems, addBytes) && len(q.items) > 0 {
				oid := q.oldestIDLocked()
				q.seq++
				if err := q.appendLocked(record{Op: "del", ID: oid, Seq: q.seq}); err != nil {
					return err
				}
				oe := q.items[oid]
				q.bytes -= oe.Bytes
				delete(q.items, oid)
			}
			if q.overLimit(addItems, addBytes) {
				return ErrFull
			}
		default:
			return ErrFull
		}
	}
	q.seq++
	if err := q.appendLocked(record{Op: "put", ID: id, Seq: q.seq, Data: data}); err != nil {
		return err
	}
	if exists {
		q.bytes -= old.Bytes
	}
	q.items[id] = entry[T]{ID: id, Seq: q.seq, Data: v, Bytes: int64(len(data))}
	q.bytes += int64(len(data))
	return nil
}

func (q *Queue[T]) Delete(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.items[id]; !ok {
		return nil
	}
	q.seq++
	if err := q.appendLocked(record{Op: "del", ID: id, Seq: q.seq}); err != nil {
		return err
	}
	old := q.items[id]
	q.bytes -= old.Bytes
	delete(q.items, id)
	return nil
}

func (q *Queue[T]) Get(id string) (T, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.items[id]
	return e.Data, ok
}

func (q *Queue[T]) List() ([]T, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	es := make([]entry[T], 0, len(q.items))
	for _, e := range q.items {
		es = append(es, e)
	}
	sort.Slice(es, func(i, j int) bool { return es[i].Seq < es[j].Seq })
	out := make([]T, 0, len(es))
	for _, e := range es {
		out = append(out, e.Data)
	}
	return out, nil
}
func (q *Queue[T]) Len() int { q.mu.Lock(); defer q.mu.Unlock(); return len(q.items) }
func (q *Queue[T]) Stats() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return Stats{Items: len(q.items), Bytes: q.bytes, MaxItems: q.opts.MaxItems, MaxBytes: q.opts.MaxBytes, Policy: q.opts.Policy}
}

func (q *Queue[T]) compactLocked() error {
	tmp := q.walPath + ".compact"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	es := make([]entry[T], 0, len(q.items))
	for _, e := range q.items {
		es = append(es, e)
	}
	sort.Slice(es, func(i, j int) bool { return es[i].Seq < es[j].Seq })
	enc := json.NewEncoder(f)
	for _, e := range es {
		b, er := json.Marshal(e.Data)
		if er != nil {
			f.Close()
			return er
		}
		if er = enc.Encode(record{Op: "put", ID: e.ID, Seq: e.Seq, Data: b}); er != nil {
			f.Close()
			return er
		}
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = q.file.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, q.walPath); err != nil {
		return err
	}
	if d, er := os.Open(q.dir); er == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	q.file, err = os.OpenFile(q.walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	q.ops = 0
	return err
}

func (q *Queue[T]) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.file != nil {
		return q.file.Close()
	}
	return nil
}
