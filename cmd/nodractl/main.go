// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type client struct {
	base, token string
	http        *http.Client
}

func main() {
	server := flag.String("server", env("NODRA_SERVER", "http://127.0.0.1:8080"), "control-plane URL")
	token := flag.String("token", os.Getenv("NODRA_ADMIN_TOKEN"), "admin token")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	c := client{strings.TrimRight(*server, "/"), *token, http.DefaultClient}
	var err error
	switch args[0] {
	case "overview":
		err = c.print("GET", "/api/v1/overview", nil)
	case "sites":
		err = sites(c, args[1:])
	case "devices":
		err = c.print("GET", "/api/v1/devices", nil)
	case "twins":
		err = twins(c, args[1:])
	case "events":
		err = c.print("GET", "/api/v1/events", nil)
	case "routes":
		err = routes(c, args[1:])
	case "deployments":
		err = deployments(c, args[1:])
	case "alerts":
		err = alerts(c, args[1:])
	case "deadletters", "dlq":
		err = deadletters(c, args[1:])
	case "version":
		err = c.print("GET", "/api/v1/version", nil)
	case "publish":
		err = publish(args[1:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
func usage() {
	fmt.Print(`nodractl — Nodra edge control plane CLI

Usage:
  nodractl [--server URL] [--token TOKEN] overview
  nodractl sites list | sites revoke SITE_ID
  nodractl devices | events | version
  nodractl twins list | twins desired DEVICE_ID --json JSON
  nodractl routes list
  nodractl routes create --name NAME --topic FILTER --target URL [--site SITE_ID] [--disabled]
  nodractl routes delete ROUTE_ID
  nodractl deployments list
  nodractl deployments create --site SITE_ID --name NAME --version VERSION --image IMAGE [--desired running|stopped]
  nodractl deployments patch DEP_ID [--version V] [--image IMG] [--desired running|stopped]
  nodractl deployments delete DEP_ID
  nodractl alerts list | alerts resolve ALERT_ID
  nodractl dlq list | dlq replay DELIVERY_ID | dlq delete DELIVERY_ID
  nodractl publish --agent URL --topic TOPIC --data JSON [--token LOCAL_TOKEN]
`)
}
func sites(c client, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		return c.print("GET", "/api/v1/sites", nil)
	}
	if args[0] == "revoke" && len(args) == 2 {
		return c.print("POST", "/api/v1/sites/"+args[1]+"/revoke", map[string]bool{})
	}
	return fmt.Errorf("unknown sites command")
}
func twins(c client, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		return c.print("GET", "/api/v1/twins", nil)
	}
	if args[0] == "desired" && len(args) >= 2 {
		fs := flag.NewFlagSet("twins desired", flag.ContinueOnError)
		raw := fs.String("json", "{}", "desired-state JSON object")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(*raw), &v); err != nil {
			return fmt.Errorf("--json: %w", err)
		}
		return c.print("PUT", "/api/v1/twins/"+args[1]+"/desired", map[string]any{"desired": v})
	}
	return fmt.Errorf("unknown twins command")
}
func routes(c client, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		return c.print("GET", "/api/v1/routes", nil)
	}
	if args[0] == "delete" && len(args) == 2 {
		return c.print("DELETE", "/api/v1/routes/"+args[1], nil)
	}
	if args[0] == "create" {
		fs := flag.NewFlagSet("routes create", flag.ContinueOnError)
		name := fs.String("name", "", "route name")
		topic := fs.String("topic", "", "topic filter")
		target := fs.String("target", "", "HTTP target")
		site := fs.String("site", "", "optional site ID")
		disabled := fs.Bool("disabled", false, "create disabled")
		retry := fs.Int("retry", 5, "max attempts")
		timeout := fs.Int("timeout", 10, "timeout seconds")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return c.print("POST", "/api/v1/routes", map[string]any{"name": *name, "topic": *topic, "target_url": *target, "site_id": *site, "method": "POST", "enabled": !*disabled, "retry_max": *retry, "timeout_seconds": *timeout})
	}
	return fmt.Errorf("unknown routes command")
}
func deployments(c client, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		return c.print("GET", "/api/v1/deployments", nil)
	}
	if args[0] == "create" {
		fs := flag.NewFlagSet("deployments create", flag.ContinueOnError)
		site := fs.String("site", "", "site ID")
		name := fs.String("name", "", "app name")
		ver := fs.String("version", "", "app version")
		image := fs.String("image", "", "container image")
		desired := fs.String("desired", "running", "running|stopped")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return c.print("POST", "/api/v1/deployments", map[string]any{"site_id": *site, "name": *name, "version": *ver, "image": *image, "desired_state": *desired})
	}
	if args[0] == "patch" && len(args) >= 2 {
		fs := flag.NewFlagSet("deployments patch", flag.ContinueOnError)
		ver := fs.String("version", "", "app version")
		image := fs.String("image", "", "container image")
		desired := fs.String("desired", "", "running|stopped")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		body := map[string]any{}
		if *ver != "" {
			body["version"] = *ver
		}
		if *image != "" {
			body["image"] = *image
		}
		if *desired != "" {
			body["desired_state"] = *desired
		}
		return c.print("PATCH", "/api/v1/deployments/"+args[1], body)
	}
	if args[0] == "delete" && len(args) == 2 {
		return c.print("DELETE", "/api/v1/deployments/"+args[1], nil)
	}
	return fmt.Errorf("unknown deployments command")
}
func alerts(c client, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		return c.print("GET", "/api/v1/alerts", nil)
	}
	if args[0] == "resolve" && len(args) == 2 {
		return c.print("POST", "/api/v1/alerts/"+args[1]+"/resolve", map[string]bool{})
	}
	return fmt.Errorf("unknown alerts command")
}
func deadletters(c client, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		return c.print("GET", "/api/v1/deadletters", nil)
	}
	if args[0] == "replay" && len(args) == 2 {
		return c.print("POST", "/api/v1/deadletters/"+args[1]+"/replay", map[string]bool{})
	}
	if args[0] == "delete" && len(args) == 2 {
		return c.print("DELETE", "/api/v1/deadletters/"+args[1], nil)
	}
	return fmt.Errorf("unknown dlq command")
}
func publish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	agent := fs.String("agent", "http://127.0.0.1:9091", "nodrad local URL")
	topic := fs.String("topic", "", "topic")
	data := fs.String("data", "{}", "JSON payload")
	token := fs.String("token", "", "local token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw := json.RawMessage([]byte(*data))
	if !json.Valid(raw) {
		return fmt.Errorf("--data must be valid JSON")
	}
	b, _ := json.Marshal(map[string]any{"topic": *topic, "payload": raw})
	req, _ := http.NewRequest("POST", strings.TrimRight(*agent, "/")+"/v1/publish", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return pretty(body)
}
func (c client) print(method, path string, v any) error {
	var body io.Reader
	if v != nil {
		b, _ := json.Marshal(v)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if v != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	if len(b) == 0 {
		return nil
	}
	return pretty(b)
}
func pretty(b []byte) error {
	var v any
	if json.Unmarshal(b, &v) == nil {
		out, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(out))
		return nil
	}
	fmt.Print(string(b))
	return nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
