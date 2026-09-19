package platform

import (
	"fmt"
	"testing"
)

func TestNumaNextRotatesDownwards(t *testing.T) {
	t.Parallel()
	// The legacy NextNuma decremented: CurrentNuma = (CurrentNuma - 1 + N) % N
	// starting at N-1. Four nodes therefore yield 3, 2, 1, 0, 3, ...
	cases := []struct {
		count int
		want  []int
	}{
		{1, []int{0, 0, 0, 0}},
		{2, []int{1, 0, 1, 0, 1, 0}},
		{3, []int{2, 1, 0, 2, 1, 0, 2}},
		{4, []int{3, 2, 1, 0, 3, 2, 1, 0, 3}},
		{8, []int{7, 6, 5, 4, 3, 2, 1, 0, 7}},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("count=%d", tc.count), func(t *testing.T) {
			t.Parallel()
			n := NewNumaWithCount(tc.count)
			for i, want := range tc.want {
				if got := n.NextNuma(); got != want {
					t.Fatalf("NextNuma() call %d = %d, want %d (count=%d)", i+1, got, want, tc.count)
				}
			}
		})
	}
}

func TestNumaPrevRotatesUpwards(t *testing.T) {
	t.Parallel()
	// PrevNuma is the mirror image: CurrentNuma = (CurrentNuma + 1) % N.
	cases := []struct {
		count int
		want  []int
	}{
		{1, []int{0, 0}},
		{2, []int{1, 0, 1, 0}},
		{4, []int{3, 0, 1, 2, 3, 0, 1, 2, 3}},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("count=%d", tc.count), func(t *testing.T) {
			t.Parallel()
			n := NewNumaWithCount(tc.count)
			for i, want := range tc.want {
				if got := n.PrevNuma(); got != want {
					t.Fatalf("PrevNuma() call %d = %d, want %d (count=%d)", i+1, got, want, tc.count)
				}
			}
		})
	}
}

func TestNumaNextThenPrevRestoresState(t *testing.T) {
	t.Parallel()
	// NextNuma steps the cursor down and PrevNuma steps it up, so one of each
	// restores the cursor: the return values differ (3 then 2), the next
	// allocation after both does not.
	n := NewNumaWithCount(4)
	if got := n.NextNuma(); got != 3 {
		t.Fatalf("NextNuma() = %d, want 3", got)
	}
	if got := n.PrevNuma(); got != 2 {
		t.Fatalf("PrevNuma() = %d, want 2", got)
	}
	if got := n.NextNuma(); got != 3 {
		t.Fatalf("NextNuma() after the pair = %d, want 3 (cursor was restored)", got)
	}
}

func TestNumaClampsInvalidCount(t *testing.T) {
	t.Parallel()
	for _, count := range []int{0, -1, -100} {
		n := NewNumaWithCount(count)
		if n.NumaCount() != 1 {
			t.Errorf("NewNumaWithCount(%d).NumaCount() = %d, want 1", count, n.NumaCount())
		}
		if got := n.NextNuma(); got != 0 {
			t.Errorf("NewNumaWithCount(%d).NextNuma() = %d, want 0", count, got)
		}
	}
}

func TestNumaSingleAlwaysNodeZero(t *testing.T) {
	t.Parallel()
	n := NewNuma(true)
	if n.NumaCount() != 1 {
		t.Fatalf("NewNuma(true).NumaCount() = %d, want 1", n.NumaCount())
	}
	for i := 0; i < 5; i++ {
		if got := n.NextNuma(); got != 0 {
			t.Fatalf("NextNuma() = %d, want 0 with singleNuma", got)
		}
	}
}

func TestX265PoolsParam(t *testing.T) {
	t.Parallel()
	// Mirrors NumaNode.X265PoolsParam: one "-" per node with "+" at the
	// selected node, joined with commas.
	cases := []struct {
		count int
		node  int
		want  string
	}{
		{1, 0, "+"},
		{2, 0, "+,-"},
		{2, 1, "-,+"},
		{3, 0, "+,-,-"},
		{3, 2, "-,-,+"},
		{4, 0, "+,-,-,-"},
		{4, 3, "-,-,-,+"},
		{8, 5, "-,-,-,-,-,+,-,-"},
		// Out of range: the legacy code raised IndexOutOfRangeException.
		{4, -1, ""},
		{4, 4, ""},
		{1, 1, ""},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("count=%d/node=%d", tc.count, tc.node), func(t *testing.T) {
			t.Parallel()
			n := NewNumaWithCount(tc.count)
			if got := n.X265PoolsParam(tc.node); got != tc.want {
				t.Errorf("X265PoolsParam(%d) with count %d = %q, want %q",
					tc.node, tc.count, got, tc.want)
			}
		})
	}
}

func TestNewNumaOnThisMachine(t *testing.T) {
	t.Parallel()
	// Exercises the platform query without asserting a specific topology:
	// every machine has at least one node, and the node returned is always in
	// range. On Windows this calls GetNumaHighestNodeNumber.
	n := NewNuma(false)
	if n.NumaCount() < 1 {
		t.Fatalf("NewNuma(false).NumaCount() = %d, want at least 1", n.NumaCount())
	}
	if got := n.NextNuma(); got < 0 || got >= n.NumaCount() {
		t.Errorf("NextNuma() = %d, out of range [0, %d)", got, n.NumaCount())
	}
}

func TestUsableCoreCount(t *testing.T) {
	t.Parallel()
	if got := UsableCoreCount(); got < 1 {
		t.Errorf("UsableCoreCount() = %d, want at least 1", got)
	}
}
