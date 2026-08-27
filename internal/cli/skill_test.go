package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	voomskill "github.com/mjrusso/voom/internal/skill"
)

func TestSkillSurfacesMatchEmbeddedMarkdown(t *testing.T) {
	want := voomskill.Markdown()
	for _, args := range [][]string{{"skill"}, {"--skill"}} {
		if got := runCmd(t, args...); got != want {
			t.Fatalf("voom %v output differs from embedded skill", args)
		}
	}
}

func TestSkillJSON(t *testing.T) {
	out := runCmd(t, "skill", "--output", "json")
	if flag := runCmd(t, "--skill", "--output", "json"); flag != out {
		t.Fatalf("--skill JSON differs from skill subcommand")
	}
	var got skillInfo
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(voomskill.Markdown()))
	if got.Name != "voom" || got.ProducerVersion != CurrentVersion().Version || got.Content != voomskill.Markdown() {
		t.Fatalf("unexpected skill envelope: %#v", got)
	}
	if want := hex.EncodeToString(sum[:]); got.ContentSHA256 != want {
		t.Fatalf("contentSha256 = %q, want %q", got.ContentSHA256, want)
	}
}

func TestSkillFlagRejectsVersion(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--skill", "--version"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected conflicting root flags to fail")
	}
}

func TestSkillWriteFailure(t *testing.T) {
	w := failingWriter{}
	if err := writeSkill(w, "test", "text"); err == nil {
		t.Fatal("expected write failure")
	}
	if err := writeSkill(w, "test", "json"); err == nil {
		t.Fatal("expected JSON write failure")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}
