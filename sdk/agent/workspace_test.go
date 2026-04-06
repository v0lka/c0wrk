package agent

import (
	"sync"
	"testing"
)

func TestSharedWorkspace_StoreAndGet(t *testing.T) {
	ws := NewSharedWorkspace()

	ws.Store("key1", "content1", "step_1")
	a, ok := ws.Get("key1")
	if !ok {
		t.Fatal("expected key1 to exist")
	}
	if a.Key != "key1" || a.Content != "content1" || a.ProducedBy != "step_1" {
		t.Errorf("unexpected artifact: %+v", a)
	}
	if a.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

func TestSharedWorkspace_GetMissing(t *testing.T) {
	ws := NewSharedWorkspace()
	_, ok := ws.Get("nonexistent")
	if ok {
		t.Error("expected ok=false for missing key")
	}
}

func TestSharedWorkspace_Overwrite(t *testing.T) {
	ws := NewSharedWorkspace()
	ws.Store("k", "v1", "s1")
	ws.Store("k", "v2", "s2")
	a, ok := ws.Get("k")
	if !ok {
		t.Fatal("expected key to exist")
	}
	if a.Content != "v2" {
		t.Errorf("Content = %q, want %q", a.Content, "v2")
	}
}

func TestSharedWorkspace_List(t *testing.T) {
	ws := NewSharedWorkspace()
	ws.Store("a", "1", "s1")
	ws.Store("b", "2", "s1")
	ws.Store("c", "3", "s2")

	items := ws.List()
	if len(items) != 3 {
		t.Errorf("List() returned %d items, want 3", len(items))
	}
}

func TestSharedWorkspace_GetByProducer(t *testing.T) {
	ws := NewSharedWorkspace()
	ws.Store("a", "1", "s1")
	ws.Store("b", "2", "s2")
	ws.Store("c", "3", "s1")

	items := ws.GetByProducer("s1")
	if len(items) != 2 {
		t.Errorf("GetByProducer(s1) returned %d items, want 2", len(items))
	}

	items = ws.GetByProducer("nonexistent")
	if len(items) != 0 {
		t.Errorf("GetByProducer(nonexistent) returned %d items, want 0", len(items))
	}
}

func TestSharedWorkspace_Clear(t *testing.T) {
	ws := NewSharedWorkspace()
	ws.Store("a", "1", "s1")
	ws.Clear()
	items := ws.List()
	if len(items) != 0 {
		t.Errorf("List() after Clear() returned %d items, want 0", len(items))
	}
}

func TestSharedWorkspace_ConcurrentAccess(t *testing.T) {
	ws := NewSharedWorkspace()
	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "k" + string(rune('A'+i%26))
			ws.Store(key, "val", "step")
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ws.List()
			ws.Get("kA")
			ws.GetByProducer("step")
		}()
	}

	wg.Wait()
}
