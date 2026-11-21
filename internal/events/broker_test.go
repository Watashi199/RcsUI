package events

import (
	"testing"
	"time"
)

func TestReplayAndResync(t *testing.T) {
	b := NewBroker()
	for i := 0; i < 3; i++ {
		b.PublishRun("R1", 1, TypeRunProgress, i, true)
	}
	sub, replay, resync := b.Subscribe("R1", "")
	if resync || len(replay) != 3 || replay[0].ID != "R1:1" {
		t.Fatalf("replay=%v resync=%v", replay, resync)
	}
	b.Unsubscribe(sub)

	sub, replay, _ = b.Subscribe("R1", "R1:2")
	if len(replay) != 1 || replay[0].ID != "R1:3" {
		t.Fatalf("resume replay=%v", replay)
	}
	b.PublishRun("R1", 1, TypeRunLog, "x", false)
	select {
	case e := <-sub.C:
		if e.ID != "R1:4" || e.Type != TypeRunLog {
			t.Errorf("live=%+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no live event")
	}
	b.Unsubscribe(sub)
	b.Unsubscribe(sub)

	if _, _, rs := b.Subscribe("R1", "R2:1"); !rs {
		t.Error("foreign stream id should resync")
	}
	if _, _, rs := b.Subscribe("R1", "R1:999"); !rs {
		t.Error("future id should resync (server restarted)")
	}
	for i := 0; i < runRing+10; i++ {
		b.PublishRun("R1", 1, TypeRunLog, i, false)
	}
	if _, _, rs := b.Subscribe("R1", "R1:1"); !rs {
		t.Error("evicted id should resync")
	}
	_, replay, rs := b.Subscribe("", "g:0")
	if rs || len(replay) != 3 {
		t.Errorf("global replay=%d resync=%v", len(replay), rs)
	}
}

func TestSlowSubscriberDropped(t *testing.T) {
	b := NewBroker()
	sub, _, _ := b.Subscribe("", "")
	for i := 0; i < subBuffer+5; i++ {
		b.PublishRun("R", 1, TypeRunState, i, true)
	}
	n := 0
	for range sub.C {
		n++
	}
	if n != subBuffer {
		t.Errorf("received %d before close", n)
	}
	if b.SubscriberCount() != 0 {
		t.Error("slow subscriber not removed")
	}
}

func TestGC(t *testing.T) {
	b := NewBroker()
	clock := time.Now()
	b.now = func() time.Time { return clock }
	b.PublishRun("R", 1, TypeRunState, nil, false)
	b.EndRun("R")
	clock = clock.Add(runRetention + time.Second)
	b.PublishRun("other", 2, TypeRunState, nil, false)
	b.mu.Lock()
	_, ok := b.streams["R"]
	b.mu.Unlock()
	if ok {
		t.Error("ended stream not collected")
	}
}
