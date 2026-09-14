// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const dayLayout = "20060102"

// FileLog persists audit entries as daily-rotated NDJSON files under
// <dataDir>/audit/audit-YYYYMMDD.log. Each Append fsyncs, matching the
// durability guarantee internal/store's WAL and internal/queue give their
// own records.
type FileLog struct {
	mu     sync.Mutex
	dir    string
	opts   Options
	cur    *os.File
	curDay string
}

func OpenFile(dataDir string, opts Options) (*FileLog, error) {
	opts = opts.withDefaults()
	dir := filepath.Join(dataDir, "audit")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &FileLog{dir: dir, opts: opts}, nil
}

func (f *FileLog) pathForDay(day string) string {
	return filepath.Join(f.dir, "audit-"+day+".log")
}

func (f *FileLog) Append(e Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e.ID == "" {
		e.ID = newID()
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	day := e.Time.Format(dayLayout)
	if f.cur == nil || f.curDay != day {
		if f.cur != nil {
			_ = f.cur.Close()
		}
		fh, err := os.OpenFile(f.pathForDay(day), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		f.cur = fh
		f.curDay = day
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err = f.cur.Write(b); err != nil {
		return err
	}
	return f.cur.Sync()
}

// dayFiles returns audit-YYYYMMDD.log entries in this dir, sorted descending
// by day, optionally restricted to [since,until] (day granularity — the
// caller still filters entries by exact timestamp).
func (f *FileLog) dayFiles(since, until time.Time) ([]string, error) {
	ents, err := os.ReadDir(f.dir)
	if err != nil {
		return nil, err
	}
	var days []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "audit-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, "audit-"), ".log")
		t, err := time.Parse(dayLayout, day)
		if err != nil {
			continue
		}
		if !since.IsZero() && t.Before(since.Truncate(24*time.Hour)) {
			continue
		}
		if !until.IsZero() && t.After(until) {
			continue
		}
		days = append(days, day)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	return days, nil
}

func (f *FileLog) Query(filter Filter) ([]Entry, string, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 250
	}
	f.mu.Lock()
	if f.cur != nil {
		_ = f.cur.Sync()
	}
	f.mu.Unlock()

	days, err := f.dayFiles(filter.Since, filter.Until)
	if err != nil {
		return nil, "", err
	}
	var all []Entry
	for _, day := range days {
		b, err := os.ReadFile(f.pathForDay(day))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var e Entry
			if err := json.Unmarshal(line, &e); err != nil {
				continue
			}
			if !filter.Since.IsZero() && e.Time.Before(filter.Since) {
				continue
			}
			if !filter.Until.IsZero() && e.Time.After(filter.Until) {
				continue
			}
			if filter.SiteID != "" && e.SiteID != filter.SiteID {
				continue
			}
			if filter.Action != "" && e.Action != filter.Action {
				continue
			}
			if filter.Actor != "" && e.Actor != filter.Actor {
				continue
			}
			all = append(all, e)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].Time.Equal(all[j].Time) {
			return all[i].Time.After(all[j].Time)
		}
		return all[i].ID > all[j].ID
	})
	if filter.Cursor != "" {
		if ct, cid, ok := decodeCursor(filter.Cursor); ok {
			// Skip everything at-or-before the cursor entry in descending
			// (Time, ID) order — i.e. resume strictly after it.
			cut := 0
			for cut < len(all) {
				e := all[cut]
				if e.Time.Before(ct) || (e.Time.Equal(ct) && e.ID < cid) {
					break
				}
				cut++
			}
			all = all[cut:]
		}
	}
	var next string
	if len(all) > limit {
		next = encodeCursor(all[limit-1].Time, all[limit-1].ID)
		all = all[:limit]
	}
	return all, next, nil
}

// Prune deletes whole day-files entirely older than the retention cutoff.
func (f *FileLog) Prune(before time.Time) error {
	days, err := f.dayFiles(time.Time{}, time.Time{})
	if err != nil {
		return err
	}
	for _, day := range days {
		t, err := time.Parse(dayLayout, day)
		if err != nil {
			continue
		}
		if t.Add(24 * time.Hour).Before(before) {
			f.mu.Lock()
			if f.curDay == day {
				f.mu.Unlock()
				continue // never prune the file we're actively writing to
			}
			f.mu.Unlock()
			_ = os.Remove(f.pathForDay(day))
		}
	}
	return nil
}

func (f *FileLog) Ping(ctx context.Context) error {
	_, err := os.Stat(f.dir)
	return err
}

func (f *FileLog) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cur != nil {
		return f.cur.Close()
	}
	return nil
}
