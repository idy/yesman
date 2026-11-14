package agent

import (
	"reflect"
	"testing"
	"time"
)

func TestSubmissionPayloadIsBoundedReadOnlyAndGenerationScoped(t *testing.T) {
	since := time.Now()
	a := &agent{info: Info{ID: "alice/lead", Since: since}}
	m := &Manager{agents: map[string]*agent{"alice/lead": a}, worktreeCleanup: map[string]bool{}}
	id := deliveryTestID(71000)
	images := []Image{testImage(), testImage()}
	m.retainRecallLocked(a, id, "  complete\ntext  ", images)
	got, err := m.SubmissionPayload(a.info.ID, id, since)
	if err != nil || got.Text != "  complete\ntext  " || !reflect.DeepEqual(got.Images, images) {
		t.Fatalf("original lost: %+v %v", got, err)
	}
	got.Images[0].Data = "mutated"
	again, _ := m.SubmissionPayload(a.info.ID, id, since)
	if !reflect.DeepEqual(again.Images, images) {
		t.Fatal("display mutated original")
	}
	if a.deliveryView(Delivery{ID: id}).CanCancel {
		t.Fatal("display payload invented queue authority")
	}
	if _, err = m.SubmissionPayload(a.info.ID, id, since.Add(time.Second)); err == nil {
		t.Fatal("old generation read")
	}
	a.deliverySealed = true
	if _, err = m.SubmissionPayload(a.info.ID, id, since); err == nil {
		t.Fatal("sealed generation read")
	}
	a.deliverySealed = false
	m.clearRecallLocked(a)
	if _, err = m.SubmissionPayload(a.info.ID, id, since); err == nil {
		t.Fatal("missing payload reconstructed")
	}
}
