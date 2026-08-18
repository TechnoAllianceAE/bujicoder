package agentruntime

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSharedMemorySummaryIsDeterministic(t *testing.T) {
	build := func() string {
		sm := NewSharedMemory()
		for i := range 12 {
			sm.Write(fmt.Sprintf("agent%d", i%4), fmt.Sprintf("key%d", i), fmt.Sprintf("value %d", i))
		}
		return sm.Summary()
	}

	want := build()
	if want == "" {
		t.Fatal("summary is empty")
	}
	for i := range 20 {
		if got := build(); got != want {
			t.Fatalf("summary differs on rebuild %d:\n--- want ---\n%s\n--- got ---\n%s", i, want, got)
		}
	}
}

func TestSharedMemorySummaryRefreshesAfterWrite(t *testing.T) {
	sm := NewSharedMemory()
	sm.Write("a", "k1", "first")
	first := sm.Summary()

	sm.Write("a", "k2", "second")
	second := sm.Summary()

	if !strings.Contains(second, "second") {
		t.Fatalf("summary did not pick up the new entry: %q", second)
	}
	if first == second {
		t.Fatal("cached summary was not invalidated by a write")
	}
}

func TestSharedMemoryWriteKeepsValidUTF8(t *testing.T) {
	sm := NewSharedMemory()
	// 3-byte runes: a raw byte cut at MaxSharedMemoryValueLen splits one.
	sm.Write("a", "k", strings.Repeat("€", MaxSharedMemoryValueLen))

	got := sm.Read("a", "k")
	if !utf8.ValidString(got) {
		t.Fatal("stored value is not valid UTF-8 after truncation")
	}
	if !strings.HasSuffix(got, "... [truncated]") {
		t.Fatal("oversized value was not truncated")
	}
}
