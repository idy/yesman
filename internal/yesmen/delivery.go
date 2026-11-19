package yesmen

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// confirmDelivery validates the lead's scope, including board prerequisites
// and recorded Needs You links, before marking the final task delivered.
func (b *Board) confirmDelivery(c DeliveryConfirmation, before []Task, handoffs Handoffs) error {
	if len(c.Related) >= maxTasks {
		return fmt.Errorf("a delivery can cover at most %d tasks", maxTasks)
	}
	old := make(map[string]Task, len(before))
	for _, task := range before {
		old[task.ID] = task
	}
	id := TaskID(c.Task)
	final, ok := old[id]
	if !ok {
		return fmt.Errorf("delivery task %q must already be on the board", c.Task)
	}
	if final.Delivered {
		return fmt.Errorf("delivery for %s was already confirmed", id)
	}
	if !final.Open() || final.Paused() {
		return fmt.Errorf("delivery task %s must be open and unpaused before marking it done", id)
	}
	finalIndex := b.index(id)
	if finalIndex < 0 || b.Tasks[finalIndex].Status != TaskDone {
		return fmt.Errorf("mark delivery task %s done in this change", id)
	}
	seen := map[string]bool{id: true}
	members := []string{id}
	for _, related := range c.Related {
		key := TaskID(related)
		if seen[key] {
			return errors.New("each delivery task ID must be listed only once")
		}
		seen[key] = true
		members = append(members, key)
	}
	for i := 0; i < len(members); i++ {
		key := members[i]
		prior, existed := old[key]
		if j := b.index(key); !existed || j < 0 || b.Tasks[j].Status != TaskDone {
			return fmt.Errorf("related or required task %q must already be on the board and be done after this change", key)
		}
		if prior.Paused() || prior.Status == TaskDropped {
			return fmt.Errorf("delivery depends on task %s, which was paused or dropped before this change", key)
		}
		// Done clears a task's wait. Also check any explicit prerequisites
		// it had at admission, even if the lead omitted them from Related.
		if w := prior.Wait; w != nil && w.Kind == WaitTasks {
			for _, dep := range w.Tasks {
				if !seen[dep] {
					seen[dep] = true
					members = append(members, dep)
				}
			}
		}
	}
	if err := checkDeliveryHandoffs(members, seen, old, handoffs); err != nil {
		return err
	}
	b.Tasks[finalIndex].Delivered = true
	return nil
}

func checkDeliveryHandoffs(members []string, scope map[string]bool, before map[string]Task, handoffs Handoffs) error {
	closedFor := map[string]bool{}
	for _, h := range handoffs.Items {
		if !scope[TaskID(h.Task)] {
			continue
		}
		if err := checkDeliveryHandoff(h); err != nil {
			return err
		}
		closedFor[TaskID(h.Task)] = true
	}
	// Changing a task to done clears its wait. Require evidence for an
	// existing human wait, including explicit IDs on otherwise unlinked
	// handoffs. An ID whose record is missing cannot establish acceptance.
	for _, id := range members {
		w := before[id].Wait
		if w == nil || w.Kind != WaitUser {
			continue
		}
		if w.Handoff == "" {
			if !closedFor[id] {
				return fmt.Errorf("task %s still records a user wait without a closed Needs You request", id)
			}
			continue
		}
		h, ok := handoffs.Find(w.Handoff)
		if !ok {
			return fmt.Errorf("the Needs You request %s for %s cannot be verified", w.Handoff, id)
		}
		if err := checkDeliveryHandoff(h); err != nil {
			return err
		}
	}
	return nil
}

func checkDeliveryHandoff(h Handoff) error {
	if h.Open() {
		return fmt.Errorf("Needs You request %s still waits on the user", h.ID)
	}
	switch h.Resolution {
	case HandoffDone, HandoffNotNeeded, HandoffResolved:
		return nil
	default:
		return fmt.Errorf("the resolution of Needs You request %s cannot be verified", h.ID)
	}
}

// writeDelivery stages the exact Board bytes before final admission. It
// also checks that the protected Needs You data still belongs to this hire
// and has the bytes validated above. App writers use the same handoff lock.
func (s *Store) writeDelivery(y Yesman, b Board, hands *handoffsFile, commit func(func() error) error) error {
	name := filepath.Base(y.BoardFile())
	temp := "." + name + "-delivery-" + rand.Text() + ".tmp"
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	out, err := hands.meta.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer hands.meta.Remove(temp)
	_, err = out.Write(append(data, '\n'))
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	workspace, meta, err := openMeta(s.root, y, &s.metaMu)
	if workspace != nil {
		defer workspace.Close()
	}
	if meta != nil {
		defer meta.Close()
	}
	if err != nil {
		return err
	}
	old, oldErr := hands.meta.Stat(".")
	current, curErr := meta.Stat(".")
	if oldErr != nil || curErr != nil || !os.SameFile(old, current) {
		return errors.New("workspace metadata changed during delivery confirmation")
	}
	if err := hands.unchanged(true); err != nil {
		return fmt.Errorf("Needs You changed during delivery confirmation: %w", err)
	}
	return commit(func() error {
		s.changing(y.Name)
		return meta.Rename(temp, name)
	})
}
