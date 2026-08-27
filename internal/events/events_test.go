package events

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEmitAndReplayFromCursor(t *testing.T) {
	dir := t.TempDir()
	emitter := NewEmitter(dir, nil)
	emitter.Emit(Event{ID: "00000000000000000000000000000001", Type: "vm", Action: "start"})
	emitter.Emit(Event{ID: "00000000000000000000000000000002", Type: "vm", Action: "stop"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var got []Event
	err := NewReader(dir).Stream(ctx, Request{Since: &Since{ID: "00000000000000000000000000000001"}}, func(ev Event) error {
		got = append(got, ev)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Action != "stop" {
		t.Fatalf("events = %#v", got)
	}

	err = NewReader(dir).Stream(ctx, Request{Since: &Since{ID: "ffffffffffffffffffffffffffffffff"}}, func(Event) error { return nil })
	if !errors.Is(err, ErrCursorUnavailable) {
		t.Fatalf("error = %v, want ErrCursorUnavailable", err)
	}
}

func TestConcurrentEmitProducesCompleteRecords(t *testing.T) {
	dir := t.TempDir()
	const writers, perWriter = 8, 40
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			emitter := NewEmitter(dir, nil)
			for j := 0; j < perWriter; j++ {
				emitter.Emit(Event{Type: "vm", Action: "test", Actor: Actor{ID: fmt.Sprintf("%d-%d", writer, j)}})
			}
		}(i)
	}
	wg.Wait()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	count := 0
	oldest := time.Unix(0, 0)
	if err := NewReader(dir).Stream(ctx, Request{Since: &Since{Time: &oldest}}, func(Event) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != writers*perWriter {
		t.Fatalf("event count = %d, want %d", count, writers*perWriter)
	}
}

func TestEmitterFailureIsReportedAndDropped(t *testing.T) {
	dir := t.TempDir()
	blocked := dir + "/blocked"
	if err := os.WriteFile(blocked, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	warnings := []string{}
	NewEmitter(blocked, func(message string) { warnings = append(warnings, message) }).Emit(Event{Type: "vm", Action: "start"})
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestParseMoment(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	got, err := ParseMoment("10m", now)
	if err != nil || !got.Equal(now.Add(-10*time.Minute)) {
		t.Fatalf("ParseMoment duration = %v, %v", got, err)
	}
	got, err = ParseMoment("2026-08-27T11:00:00Z", now)
	if err != nil || !got.Equal(now.Add(-time.Hour)) {
		t.Fatalf("ParseMoment timestamp = %v, %v", got, err)
	}
}

func TestUntilScansPastNewerTimestamp(t *testing.T) {
	dir := t.TempDir()
	emitter := NewEmitter(dir, nil)
	emitter.Emit(Event{ID: "00000000000000000000000000000001", TimeNano: 300, Type: "vm", Action: "newer"})
	emitter.Emit(Event{ID: "00000000000000000000000000000002", TimeNano: 100, Type: "vm", Action: "older"})
	since := time.Unix(0, 0)
	until := time.Unix(0, 200)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var got []Event
	if err := NewReader(dir).Stream(ctx, Request{Since: &Since{Time: &since}, Until: &until}, func(ev Event) error {
		got = append(got, ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Action != "older" {
		t.Fatalf("events = %#v", got)
	}
}

func TestRotationRetainsReplayContinuity(t *testing.T) {
	dir := t.TempDir()
	emitter := NewEmitter(dir, nil)
	payload := strings.Repeat("x", 7000)
	for i := 0; i < 650; i++ {
		emitter.Emit(Event{Type: "vm", Action: "test", Actor: Actor{ID: fmt.Sprint(i), Attributes: map[string]string{"payload": payload}}})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	oldest := time.Unix(0, 0)
	count := 0
	if err := NewReader(dir).Stream(ctx, Request{Since: &Since{Time: &oldest}}, func(Event) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 650 {
		t.Fatalf("event count = %d, want 650", count)
	}
}

func TestTailDrainsRotatedGeneration(t *testing.T) {
	dir := t.TempDir()
	emitter := NewEmitter(dir, nil)
	emitter.Emit(Event{Type: "vm", Action: "anchor"})
	reader := NewReader(dir)
	reader.poll = 500 * time.Millisecond
	logs, err := reader.snapshot(true)
	if err != nil {
		t.Fatal(err)
	}
	current := logs[0]
	anchor, err := readEvent(current.r, &current.pending)
	if err != nil {
		t.Fatal(err)
	}
	current.lastID = anchor.ID

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const count = 650
	got := make(chan Event, count)
	done := make(chan error, 1)
	go func() {
		done <- reader.tail(ctx, current, nil, func(ev Event) error {
			got <- ev
			if len(got) == count {
				cancel()
			}
			return nil
		})
	}()
	time.Sleep(10 * time.Millisecond)
	payload := strings.Repeat("x", 7000)
	for i := 0; i < count; i++ {
		emitter.Emit(Event{Type: "vm", Action: "test", Actor: Actor{ID: fmt.Sprint(i), Attributes: map[string]string{"payload": payload}}})
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tail did not finish")
	}
	if len(got) != count {
		t.Fatalf("event count = %d, want %d", len(got), count)
	}
}

func TestMissingLogWaitIncludesFirstEvent(t *testing.T) {
	dir := t.TempDir()
	reader := NewReader(dir)
	reader.poll = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan Event, 1)
	done := make(chan error, 1)
	go func() {
		done <- reader.Stream(ctx, Request{}, func(ev Event) error {
			got <- ev
			cancel()
			return nil
		})
	}()
	time.Sleep(2 * reader.poll)
	NewEmitter(dir, nil).Emit(Event{Type: "vm", Action: "create"})
	select {
	case ev := <-got:
		if ev.Action != "create" {
			t.Fatalf("event = %#v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("first event was not delivered")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMissingLogWithTimestampWaits(t *testing.T) {
	dir := t.TempDir()
	reader := NewReader(dir)
	reader.poll = 5 * time.Millisecond
	since := time.Now().Add(-time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan Event, 1)
	done := make(chan error, 1)
	go func() {
		done <- reader.Stream(ctx, Request{Since: &Since{Time: &since}}, func(ev Event) error {
			got <- ev
			cancel()
			return nil
		})
	}()
	time.Sleep(2 * reader.poll)
	NewEmitter(dir, nil).Emit(Event{Type: "vm", Action: "create"})
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("event was not delivered")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLiveSnapshotDeliversSubsequentAppend(t *testing.T) {
	dir := t.TempDir()
	emitter := NewEmitter(dir, nil)
	emitter.Emit(Event{Type: "vm", Action: "old"})
	reader := NewReader(dir)
	logs, err := reader.snapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogs(logs)
	emitter.Emit(Event{Type: "vm", Action: "new"})
	ev, err := readEvent(logs[0].r, &logs[0].pending)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Action != "new" {
		t.Fatalf("event = %#v", ev)
	}
}
