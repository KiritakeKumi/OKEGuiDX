package platform

import (
	"strings"
	"sync"
)

// Numa hands NUMA node numbers to workers, reproducing the round-robin
// assignment of the legacy NumaNode class.
//
// The node count is fixed when the allocator is built: the machine's highest
// node number plus one, or one when the singleNuma switch is set. The first
// allocation starts at the highest node, so a four-node machine yields
// 3, 2, 1, 0, 3, ... from NextNuma. Allocators are safe for concurrent use;
// the legacy static fields were not.
type Numa struct {
	mu      sync.Mutex
	count   int
	current int
}

// NewNuma builds the allocator for this machine. When single is true the
// machine is treated as having exactly one node, mirroring the singleNuma
// configuration option.
func NewNuma(single bool) *Numa {
	if single {
		return NewNumaWithCount(1)
	}
	return NewNumaWithCount(numaNodeCount())
}

// NewNumaWithCount builds the allocator for a known node count. Callers that
// already hold a node.Capabilities should pass its NUMANodes so the allocator
// and the advertised capability cannot disagree. A count below one is
// clamped to one.
func NewNumaWithCount(count int) *Numa {
	if count < 1 {
		count = 1
	}
	return &Numa{count: count, current: count - 1}
}

// NumaCount is the number of nodes the allocator cycles through.
func (n *Numa) NumaCount() int { return n.count }

// NextNuma returns the current node and steps to the previous node number.
// The legacy NextNuma decremented, so with three nodes it yields 2, 1, 0, 2.
func (n *Numa) NextNuma() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	res := n.current
	n.current = (n.current - 1 + n.count) % n.count
	return res
}

// PrevNuma returns the current node and steps to the next node number.
func (n *Numa) PrevNuma() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	res := n.current
	n.current = (n.current + 1) % n.count
	return res
}

// X265PoolsParam renders the x265 --pools value for one node: one entry per
// node, "-" everywhere and "+" at currentNuma, joined with commas, exactly as
// NumaNode.X265PoolsParam built it. An out-of-range node yields an empty
// string instead of the IndexOutOfRangeException the legacy code raised.
func (n *Numa) X265PoolsParam(currentNuma int) string {
	if currentNuma < 0 || currentNuma >= n.count {
		return ""
	}
	parts := make([]string, n.count)
	for i := range parts {
		parts[i] = "-"
	}
	parts[currentNuma] = "+"
	return strings.Join(parts, ",")
}
