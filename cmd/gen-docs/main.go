// Command gen-docs regenerates the Markdown command reference under docs/commands from the cobra tree.
package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra/doc"

	"github.com/mjrusso/voom/internal/cli"
)

func main() {
	out := filepath.Join("docs", "commands")
	if err := os.RemoveAll(out); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}
	cmd := cli.NewRootCommand()
	cmd.DisableAutoGenTag = true
	if err := doc.GenMarkdownTree(cmd, out); err != nil {
		log.Fatal(err)
	}
	if err := normalizeMarkdown(out); err != nil {
		log.Fatal(err)
	}
}

func normalizeMarkdown(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		trimmed := strings.TrimRight(string(b), "\n") + "\n"
		return os.WriteFile(path, []byte(trimmed), 0o644)
	})
}
