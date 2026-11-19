package crew

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Patch limits keep a user-editable file small enough to validate in full.
const (
	MaxPatchBytes = 1 << 20
	MaxPatchRows  = 2000
	SourcePatch   = "patch"
	PatchHeader   = "provider,model_id,effort,intelligence_index,tok_per_s,coding_mean_pct,cost_per_task_usd,time_min_per_task\n"
)

// PatchKey deliberately does not use MemberKey, ParseKey or AA normalization:
// full IDs may contain slashes/colons, and a default effort is not a named effort.
type PatchKey struct{ Provider, Model, Effort string }
type PatchMetrics struct{ Intelligence, Speed, Coding, CostPerTask, MinutesPerTask *float64 }
type Patch map[PatchKey]PatchMetrics

// ParsePatch accepts only the stable, structured CSV schema. Blank numeric
// cells inherit; a literal zero overrides. "default" names the empty effort.
func ParsePatch(data []byte) (Patch, error) {
	if len(data) > MaxPatchBytes {
		return nil, fmt.Errorf("CSV exceeds %d bytes", MaxPatchBytes)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("CSV must be UTF-8")
	}
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff")))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("row 1: read CSV header: %w", err)
	}
	columns := strings.Split(strings.TrimSpace(PatchHeader), ",")
	index := map[string]int{}
	for i, name := range header {
		if !slices.Contains(columns, name) {
			return nil, fmt.Errorf("row 1, column %d: unknown header %q; use the blank template", i+1, name)
		}
		if _, ok := index[name]; ok {
			return nil, fmt.Errorf("row 1, column %s: duplicate header", name)
		}
		index[name] = i
	}
	for _, name := range columns {
		if _, ok := index[name]; !ok {
			return nil, fmt.Errorf("row 1: missing column %s; use the blank template", name)
		}
	}
	out := Patch{}
	seen := map[PatchKey]int{}
	for n := 0; ; n++ {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("CSV: %w", err)
		}
		line, _ := r.FieldPos(0)
		if n >= MaxPatchRows {
			return nil, fmt.Errorf("row %d: CSV exceeds %d score rows", line, MaxPatchRows)
		}
		if len(row) != len(columns) {
			return nil, fmt.Errorf("row %d: expected %d columns, got %d", line, len(columns), len(row))
		}
		cell := func(name string) string { return row[index[name]] }
		for _, name := range columns[:3] {
			v := cell(name)
			if v == "" || len(v) > 512 || strings.TrimSpace(v) != v || strings.ContainsFunc(v, unicode.IsControl) {
				return nil, fmt.Errorf("row %d, column %s: use a nonempty exact key (at most 512 bytes, no surrounding whitespace or control characters)", line, name)
			}
		}
		effort := cell("effort")
		if effort == "default" {
			effort = ""
		} else if !slices.Contains(Levels, effort) {
			return nil, fmt.Errorf("row %d, column effort: use default, off, minimal, low, medium, high, xhigh or max", line)
		}
		key := PatchKey{cell("provider"), cell("model_id"), effort}
		if earlier, ok := seen[key]; ok {
			return nil, fmt.Errorf("row %d, columns provider/model_id/effort: duplicate key from row %d", line, earlier)
		}
		seen[key] = line
		var p PatchMetrics
		fields := []**float64{&p.Intelligence, &p.Speed, &p.Coding, &p.CostPerTask, &p.MinutesPerTask}
		for i, target := range fields {
			name := columns[i+3]
			v := strings.TrimSpace(cell(name))
			if v == "" {
				continue
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
				return nil, fmt.Errorf("row %d, column %s: enter a finite number at least 0, or leave blank to inherit", line, name)
			}
			if (i == 0 || i == 2) && f > 100 {
				return nil, fmt.Errorf("row %d, column %s: must be at most 100", line, name)
			}
			*target = &f
		}
		out[key] = p
	}
	return out, nil
}

// apply overlays only supplied metrics and keeps the fallback's AA metadata.
// FieldSources describes available fields only; absent fields stay unavailable.
func (p Patch) apply(m *Member, o Option) {
	v, ok := p[PatchKey{o.Provider, o.Model, o.Effort}]
	if !ok {
		return
	}
	values := []*float64{v.Intelligence, v.Speed, v.Coding, v.CostPerTask, v.MinutesPerTask}
	if slices.IndexFunc(values, func(v *float64) bool { return v != nil }) < 0 {
		return
	}
	m.FallbackSource = m.Source
	m.Estimate = o.Score
	if m.Score == nil {
		m.Score = &Score{IntelligenceMissing: true}
	}
	s := m.Score
	m.FieldSources = map[string]string{}
	keys := []string{"intelligence", "speed", "coding", "costPerTask", "minutesPerTask"}
	current := []*float64{nil, s.Speed, s.Coding, s.CostPerTask, s.MinutesPerTask}
	if !s.IntelligenceMissing {
		current[0] = &s.Intelligence
	}
	for i, key := range keys {
		if current[i] != nil {
			m.FieldSources[key] = m.Source
		}
		if values[i] != nil {
			current[i] = values[i]
			m.FieldSources[key] = SourcePatch
		}
	}
	if current[0] != nil {
		s.Intelligence, s.IntelligenceMissing = *current[0], false
	}
	s.Speed, s.Coding, s.CostPerTask, s.MinutesPerTask = current[1], current[2], current[3], current[4]
	m.Source = SourcePatch
}

// FieldSource is the provenance of an available metric, including mixed scores.
func (m Member) FieldSource(key string) string {
	if m.Source == SourcePatch {
		return m.FieldSources[key]
	}
	return m.Source
}
