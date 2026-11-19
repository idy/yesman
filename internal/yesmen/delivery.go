package yesmen

import (
	"errors"
	"fmt"
)

// confirmDelivery validates the lead's explicit scope against the very same
// board update that closes the final task. No readiness is inferred from the
// rest of the board, from wait relationships, or from the agents' statuses.
func (b *Board) confirmDelivery(c DeliveryConfirmation, before []Task) error {
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
	b.Tasks[finalIndex].Delivered = true
	return nil
}
