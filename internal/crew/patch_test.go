package crew

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPatchValidation(t *testing.T) {
	good := PatchHeader + "route,full/model:tag,default,0,12.5,,0,\n"
	p, err := ParsePatch([]byte(good))
	if err != nil || len(p) != 1 {
		t.Fatalf("parse: %v %v", p, err)
	}
	v := p[PatchKey{"route", "full/model:tag", ""}]
	if v.Intelligence == nil || *v.Intelligence != 0 || v.Coding != nil || v.CostPerTask == nil || *v.CostPerTask != 0 {
		t.Fatalf("blank/zero: %+v", v)
	}
	for name, text := range map[string]string{
		"duplicate":        good + "route,full/model:tag,default,,10,,,\n",
		"empty effort":     PatchHeader + "route,m,,1,,,,\n",
		"unknown effort":   PatchHeader + "route,m,DEFAULT,1,,,,\n",
		"negative":         PatchHeader + "route,m,off,1,-1,,,\n",
		"nan":              PatchHeader + "route,m,off,NaN,,,,\n",
		"infinity":         PatchHeader + "route,m,off,1,Inf,,,\n",
		"overflow":         PatchHeader + "route,m,off,1,1e999,,,\n",
		"coding":           PatchHeader + "route,m,off,1,,101,,\n",
		"intelligence":     PatchHeader + "route,m,off,101,,,,\n",
		"key space":        PatchHeader + " route,m,off,1,,,,\n",
		"missing column":   "provider,model_id,effort\n",
		"unknown column":   strings.Replace(PatchHeader, "tok_per_s", "notes", 1),
		"duplicate header": strings.Replace(PatchHeader, "tok_per_s", "effort", 1),
		"field count":      PatchHeader + "route,m,off,1\n",
		"utf8":             PatchHeader + string([]byte{0xff}),
		"byte limit":       strings.Repeat("a", MaxPatchBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePatch([]byte(text)); err == nil {
				t.Fatal("accepted invalid CSV")
			}
		})
	}
	if _, err := ParsePatch([]byte(good + "route,full/model:tag,default,,,,,\n")); err == nil || !strings.Contains(err.Error(), "row 3") || !strings.Contains(err.Error(), "row 2") {
		t.Fatalf("duplicate diagnosis: %v", err)
	}
	if _, err := ParsePatch([]byte(PatchHeader)); err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePatch([]byte("\ufeff" + strings.ReplaceAll(good, "\n", "\r\n"))); err != nil {
		t.Fatal(err)
	}
	var rows strings.Builder
	rows.WriteString(PatchHeader)
	// Distinct keys reach the row limit, not the duplicate check.
	for i := 0; i <= MaxPatchRows; i++ {
		fmt.Fprintf(&rows, "r,m%d,off,,,,,\n", i)
	}
	if _, err := ParsePatch([]byte(rows.String())); err == nil || !strings.Contains(err.Error(), "2000 score rows") {
		t.Fatalf("row budget: %v", err)
	}
}

func TestPatchUpgradeAndExactKeys(t *testing.T) {
	f := func(x float64) *float64 { return &x }
	before := Snapshot{Date: "2026-01-01", Scores: []Score{{Name: "Known", Base: "Known", Intelligence: 40, Speed: f(10), MinutesPerTask: f(0)}}}
	after := Snapshot{Date: "2026-02-01", Scores: []Score{{Name: "Known", Base: "Known", Intelligence: 50, Speed: f(20), Coding: f(60), MinutesPerTask: f(0)}, {Name: "New", Base: "New", Intelligence: 70}}}
	data := []byte(PatchHeader + "r,full/known,high,0,,,,\nr,private,off,,123,,,\nr,default,default,12,,,,\nr,estimate,low,,0,,,\n")
	path := filepath.Join(t.TempDir(), "model-score-patch.csv")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePatch(persisted)
	if err != nil {
		t.Fatal(err)
	}
	models := []Model{{Provider: "r", ID: "full/known", Name: "Known", Levels: []string{"high"}}}
	config := Config{Options: []Option{{Provider: "r", Model: "full/known", Effort: "high"}, {Provider: "r", Model: "private", Effort: "off"}, {Provider: "r", Model: "new", Effort: "off"}}}
	old := Resolve(config, nil, before, p)
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != string(data) {
		t.Fatalf("upgrade changed persisted patch bytes: %v", err)
	}
	reloaded, _ := ParsePatch(unchanged) // same persisted bytes, new built-in snapshot
	next := Resolve(config, nil, after, reloaded)
	if old[0].Score.Intelligence != 0 || next[0].Score.Intelligence != 0 || *old[0].Score.Speed != 10 || *next[0].Score.Speed != 20 || *next[0].Score.Coding != 60 {
		t.Fatalf("upgrade: %+v %+v", old[0].Score, next[0].Score)
	}
	if next[1].Score == nil || !next[1].Score.IntelligenceMissing || *next[1].Score.Speed != 123 || old[2].Score != nil || next[2].Score.Intelligence != 70 {
		t.Fatalf("new/patch-only: %+v", next)
	}
	if len(next) != len(config.Options) || len(models) != 1 {
		t.Fatal("patch invented membership")
	}
	if next[0].FieldSource("intelligence") != SourcePatch || next[0].FieldSource("minutesPerTask") != SourceAA || next[0].Score.Effort != "" {
		t.Fatal("lost provenance")
	}
	for _, o := range []Option{{Provider: "other", Model: "full/known", Effort: "high"}, {Provider: "r", Model: "known", Effort: "high"}, {Provider: "r", Model: "full/known", Effort: "low"}, {Provider: "r", Model: "FULL/known", Effort: "high"}, {Provider: "r", Model: "default", Effort: "off"}} {
		c := Config{Options: []Option{o}}
		if !reflect.DeepEqual(Members(c, nil, before), Resolve(c, nil, before, p)) {
			t.Fatalf("non-exact match: %+v", o)
		}
	}
	c := Config{Options: []Option{{Provider: "r", Model: "default"}, {Provider: "r", Model: "estimate", Effort: "low", Score: &Score{Intelligence: 15, Coding: f(16)}}}}
	got := Resolve(c, nil, before, p)
	if got[0].Score.Intelligence != 12 || got[1].Score.Intelligence != 15 || *got[1].Score.Speed != 0 || got[1].FieldSource("coding") != SourceUser {
		t.Fatalf("default/estimate: %+v", got)
	}
	blank, _ := ParsePatch([]byte(PatchHeader + "r,full/known,high,,,,,\n"))
	if !reflect.DeepEqual(Members(config, nil, before), Resolve(config, nil, before, blank)) {
		t.Fatal("blank patch changes existing behavior")
	}
	if before.Scores[0].Intelligence != 40 || *before.Scores[0].Speed != 10 {
		t.Fatal("mutated base")
	}
}
