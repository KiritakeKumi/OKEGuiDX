package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// TaskID identifies a task for the whole lifetime of the queue. It is a UUID
// (see CLUSTER.md §2, reservation 3) rather than the legacy auto-incrementing
// integer, so that a task keeps its identity across process restarts and,
// later, across machines.
type TaskID string

// NewTaskID returns a random (version 4, variant 1) UUID string.
func NewTaskID() TaskID {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; if it somehow does,
		// a zero id is still better than panicking in the scheduler.
		return TaskID("00000000-0000-4000-8000-000000000000")
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1

	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return TaskID(out[:])
}

// String implements fmt.Stringer.
func (id TaskID) String() string { return string(id) }

// IsZero reports whether the id is empty.
func (id TaskID) IsZero() bool { return id == "" }

// ParseTaskID validates a textual UUID.
func ParseTaskID(s string) (TaskID, error) {
	if len(s) != 36 {
		return "", fmt.Errorf("model: task id %q is not a UUID", s)
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return "", fmt.Errorf("model: task id %q has a malformed separator", s)
			}
		default:
			if !isHex(c) {
				return "", fmt.Errorf("model: task id %q contains a non-hex digit", s)
			}
		}
	}
	return TaskID(s), nil
}

func isHex(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
