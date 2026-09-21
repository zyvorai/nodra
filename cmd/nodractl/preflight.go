// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (c client) get(path string) (int, []byte, error) {
	hc := c.http
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		return 0, nil, err
	}
	if c.token != "" && !strings.HasPrefix(path, "/healthz") && !strings.HasPrefix(path, "/readyz") && path != "/api/v1/version" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

func preflight(c client, doctor bool) error {
	checks := []struct {
		name, path, want string
	}{
		{"healthz", "/healthz", `"status":"ok"`},
		{"readyz", "/readyz", "ready"},
		{"version", "/api/v1/version", `"version"`},
	}
	var failed bool
	for _, ck := range checks {
		code, body, err := c.get(ck.path)
		if err != nil || code != 200 || !strings.Contains(string(body), ck.want) {
			failed = true
			fmt.Printf("fail  %s\n", ck.name)
			continue
		}
		fmt.Printf("ok    %s\n", ck.name)
	}
	if doctor {
		if c.token == "" {
			fmt.Println("warn  overview skipped (no admin token)")
		} else if code, body, err := c.get("/api/v1/overview"); err != nil || code != 200 {
			failed = true
			fmt.Println("fail  overview")
		} else {
			fmt.Println("ok    overview")
			var ov struct {
				DeadLetters int `json:"dead_letters"`
				OpenAlerts  int `json:"open_alerts"`
			}
			_ = json.Unmarshal(body, &ov)
			if ov.DeadLetters > 0 {
				fmt.Printf("warn  dead_letters=%d\n", ov.DeadLetters)
			}
			if ov.OpenAlerts > 0 {
				fmt.Printf("warn  open_alerts=%d\n", ov.OpenAlerts)
			}
		}
	}
	if failed {
		return fmt.Errorf("preflight failed")
	}
	return nil
}

func supportBundle(c client, args []string) error {
	fs := flag.NewFlagSet("support-bundle", flag.ContinueOnError)
	out := fs.String("out", "nodra-support", "directory to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		return err
	}
	for _, path := range []string{"/healthz", "/readyz", "/api/v1/version"} {
		code, body, err := c.get(path)
		name := strings.TrimPrefix(path, "/")
		name = strings.ReplaceAll(name, "/", "-")
		if err != nil {
			body = []byte(err.Error())
		}
		wrapped, _ := json.MarshalIndent(map[string]any{"status": code, "body": json.RawMessage(safeJSON(body))}, "", "  ")
		if err := os.WriteFile(filepath.Join(*out, name+".json"), wrapped, 0o640); err != nil {
			return err
		}
	}
	note := "Tokens and passwords are not included. This bundle is a point-in-time read of health endpoints.\n"
	if c.token != "" {
		code, body, err := c.get("/api/v1/overview")
		if err == nil && code == 200 {
			_ = os.WriteFile(filepath.Join(*out, "overview.json"), body, 0o640)
		}
	}
	return os.WriteFile(filepath.Join(*out, "README.txt"), []byte(note), 0o640)
}

func safeJSON(b []byte) []byte {
	if json.Valid(b) {
		return b
	}
	out, _ := json.Marshal(string(b))
	return out
}
