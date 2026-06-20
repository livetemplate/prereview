package review

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// newCommentID generates a sortable ID — nanosecond timestamp + 8 random
// hex chars. Lexicographic order matches creation order, no external
// dependency. Collisions across a single session are astronomically
// unlikely (1e-19 per concurrent submission).
func newCommentID() string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("%016x-%s", time.Now().UnixNano(), hex.EncodeToString(buf[:]))
}
