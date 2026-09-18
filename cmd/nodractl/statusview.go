// Cilium-style colorful status banner shared by edge operator CLIs.
package main

import (
	"fmt"
	"strings"
)

const (
	ansiRed     = "\033[31m"
	ansiYellow  = "\033[33m"
	ansiBlue    = "\033[34m"
	ansiGreen   = "\033[32m"
	ansiMagenta = "\033[35m"
	ansiCyan    = "\033[36m"
	ansiReset   = "\033[0m"
)

// Component is one logo-row summary.
type Component struct {
	Errors   int
	Warnings int
	Disabled bool
}

func (c Component) Summary() string {
	var parts []string
	if c.Errors > 0 {
		parts = append(parts, fmt.Sprintf("❌ %s%d errors%s", ansiRed, c.Errors, ansiReset))
	}
	if c.Warnings > 0 {
		parts = append(parts, fmt.Sprintf("⚠️  %s%d warnings%s", ansiYellow, c.Warnings, ansiReset))
	}
	if c.Disabled {
		parts = append(parts, fmt.Sprintf("ℹ️  %sdisabled%s", ansiCyan, ansiReset))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("✅ %sOK%s", ansiGreen, ansiReset)
	}
	return strings.Join(parts, ", ")
}

// Feature is a post-banner capability row.
type Feature struct {
	Name  string
	State Component
}

// StatusView is the Cilium-shaped platform status.
type StatusView struct {
	// Labels must be length 5 (KubeVirt-style logo rows).
	Labels     [5]string
	Components [5]Component
	Body       [][3]string
	Features   []Feature
	Collection []string
}

func (v StatusView) Format() string {
	var b strings.Builder
	sums := make([]string, 5)
	for i := range v.Components {
		sums[i] = v.Components[i].Summary()
	}
	fmt.Fprintf(&b, "%s /¯¯\\\n", ansiYellow)
	fmt.Fprintf(&b, "%s /¯¯%s\\__/%s¯¯\\%s\t%s:\t%s\n", ansiCyan, ansiYellow, ansiGreen, ansiReset, v.Labels[0], sums[0])
	fmt.Fprintf(&b, "%s \\__%s/¯¯\\%s__/%s\t%s:\t%s\n", ansiCyan, ansiRed, ansiGreen, ansiReset, v.Labels[1], sums[1])
	fmt.Fprintf(&b, "%s /¯¯%s\\__/%s¯¯\\%s\t%s:\t%s\n", ansiGreen, ansiRed, ansiMagenta, ansiReset, v.Labels[2], sums[2])
	fmt.Fprintf(&b, "%s \\__%s/¯¯\\%s__/%s\t%s:\t%s\n", ansiGreen, ansiBlue, ansiMagenta, ansiReset, v.Labels[3], sums[3])
	fmt.Fprintf(&b, "%s%s%s \\__/%s\t%s:\t%s\n\n", ansiBlue, ansiBlue, ansiBlue, ansiReset, v.Labels[4], sums[4])

	rows := append([][3]string{}, v.Body...)
	header := "✨ Features"
	for _, f := range v.Features {
		rows = append(rows, [3]string{header, f.Name, f.State.Summary()})
		header = ""
	}
	header = "🔌 Collection:"
	for _, err := range v.Collection {
		rows = append(rows, [3]string{header, "", err})
		header = ""
	}
	b.WriteString(alignRows(rows))
	return b.String()
}

func alignRows(rows [][3]string) string {
	if len(rows) == 0 {
		return ""
	}
	widths := [3]int{}
	plain := make([][3]string, len(rows))
	for i, row := range rows {
		for c := 0; c < 3; c++ {
			plain[i][c] = stripANSI(row[c])
			if n := len(plain[i][c]); n > widths[c] {
				widths[c] = n
			}
		}
	}
	var b strings.Builder
	for i, row := range rows {
		for c := 0; c < 3; c++ {
			b.WriteString(row[c])
			if c+1 < 3 {
				pad := widths[c] - len(plain[i][c]) + 4
				if pad < 4 {
					pad = 4
				}
				b.WriteString(strings.Repeat(" ", pad))
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 'A' || (s[i] > 'Z' && s[i] < 'a') || s[i] > 'z') {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func okFeature(name string, on bool) Feature {
	return Feature{Name: name, State: Component{Disabled: !on}}
}
