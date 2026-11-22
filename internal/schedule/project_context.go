package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"sort"
	"strings"

	"github.com/idy/yesman/internal/tasklog"
	"github.com/idy/yesman/internal/yesmen"
)

// VisitContextRecords reads saved plans and retained runs through the Tasks
// readers, without loading the scheduler or copying its Board projection.
// The visitor only gets the decoded model. Callers must project public fields.
func VisitContextRecords(ctx context.Context, y yesmen.Yesman, id string, plan func(Plan), run func(Run)) (partial bool, err error) {
	dir, err := openSchedules(y, false)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer dir.Close()
	if !runIDRE.MatchString(id) {
		// Check aliases as well as the scheduler's ordinary file validation.
		if f, err := tasklog.Open(dir, "plans.json"); err == nil {
			data, err := io.ReadAll(io.LimitReader(f, maxPlansFile+1))
			valid := f.Validate() == nil
			f.Close()
			if err == nil && valid && len(data) <= maxPlansFile {
				var plans plansFile
				plans, err = decodePlans(data)
				for _, p := range plans.Plans {
					if err == nil && (id == "" || id == p.ID) {
						plan(p)
					}
				}
			}
			partial = err != nil || !valid || len(data) > maxPlansFile
		} else if !errors.Is(err, fs.ErrNotExist) {
			partial = true
		}
		if id != "" {
			return partial, nil
		}
	}
	rd, err := openRuns(dir, false)
	if errors.Is(err, fs.ErrNotExist) {
		return partial, nil
	}
	if err != nil {
		return true, nil
	}
	defer rd.Close()
	var names []string
	if id != "" {
		names = []string{id + ".json"}
	} else {
		f, err := rd.Open(".")
		if err != nil {
			return true, nil
		}
		names, err = f.Readdirnames(4097)
		f.Close()
		if err != nil && !errors.Is(err, io.EOF) || len(names) > 4096 {
			return true, nil
		}
		sort.Strings(names)
	}
	var scanned int64
	for _, name := range names {
		if ctx.Err() != nil || scanned >= 8<<20 {
			return true, nil
		}
		key, ok := strings.CutSuffix(name, ".json")
		if !ok || !runIDRE.MatchString(key) {
			continue
		}
		f, err := tasklog.Open(rd, name)
		if errors.Is(err, fs.ErrNotExist) && id != "" {
			continue
		}
		if err != nil {
			partial = true
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, maxRunFile+1))
		valid := f.Validate() == nil
		f.Close()
		scanned += int64(len(data))
		var r Run
		planID, _, _ := strings.Cut(key, "-")
		if !valid || err != nil || len(data) > maxRunFile || json.Unmarshal(data, &r) != nil || r.ID != key || r.Plan != planID {
			partial = true
			continue
		}
		run(r)
	}
	return partial, nil
}
