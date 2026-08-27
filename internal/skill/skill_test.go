package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestSHA256(t *testing.T) {
	sum := sha256.Sum256([]byte(Markdown()))
	want := hex.EncodeToString(sum[:])
	if got := SHA256(); got != want {
		t.Fatalf("SHA256() = %q, want %q", got, want)
	}
	if len(Markdown()) == 0 || !strings.HasPrefix(Markdown(), "---\n") {
		t.Fatal("embedded skill is empty or lacks frontmatter")
	}
}
