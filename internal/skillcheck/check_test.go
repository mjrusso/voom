package skillcheck

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestValidate(t *testing.T) {
	valid := skillWith("```bash\nvoom image inspect <name> --output json\n```")
	tests := []struct {
		name     string
		markdown string
		wantErr  string
	}{
		{name: "valid", markdown: valid},
		{name: "valid shorthand", markdown: skillWith("```bash\nvoom image inspect <name> -c 2\n```")},
		{name: "bad long flag", markdown: skillWith("```bash\nvoom image inspect <name> --invented\n```"), wantErr: "unknown flag: --invented"},
		{name: "bad short flag", markdown: skillWith("```bash\nvoom image inspect <name> -z\n```"), wantErr: "unknown shorthand flag"},
		{name: "bad flag value", markdown: skillWith("```bash\nvoom image inspect <name> --count many\n```"), wantErr: "invalid argument"},
		{name: "missing flag value", markdown: skillWith("```bash\nvoom image inspect <name> --output\n```"), wantErr: "flag needs an argument"},
		{name: "missing argument", markdown: skillWith("```bash\nvoom image inspect\n```"), wantErr: "accepts 1 arg"},
		{name: "extra argument", markdown: skillWith("```bash\nvoom image inspect one two\n```"), wantErr: "accepts 1 arg"},
		{name: "missing required flag", markdown: skillWith("```bash\nvoom create scratch\n```"), wantErr: "requires --image"},
		{name: "required flag does not leak", markdown: skillWith("```bash\nvoom create one --image base\nvoom create two\n```"), wantErr: "requires --image"},
		{name: "bad command", markdown: skillWith("```bash\nvoom image invented\n```"), wantErr: "unknown command"},
		{name: "ignored bad flag", markdown: skillWith("```bash\nvoom image inspect <name> --invented # skill-check: ignore\n```")},
		{name: "complex shell", markdown: skillWith("```bash\nvoom list | jq .\n```"), wantErr: "complex shell command"},
		{name: "unclosed bash fence", markdown: skillWith("```bash\nvoom image inspect <name>"), wantErr: "unclosed bash code fence"},
		{name: "extra frontmatter", markdown: "---\nname: voom\ndescription: test\nversion: 1\n---\nbody\n", wantErr: "exactly name and description"},
		{name: "wrong name", markdown: "---\nname: other\ndescription: test\n---\nbody\n", wantErr: "name must be voom"},
		{name: "long description", markdown: skillWithDescription(strings.Repeat("x", 501)), wantErr: "exceeds 500"},
		{name: "too large", markdown: skillWith(strings.Repeat("x", maxSize)), wantErr: "maximum"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.markdown, testRoot())
			if tt.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func testRoot() *cobra.Command {
	root := &cobra.Command{Use: "voom"}
	root.PersistentFlags().String("output", "text", "")
	image := &cobra.Command{Use: "image"}
	inspect := &cobra.Command{Use: "inspect <name>", Args: cobra.ExactArgs(1)}
	inspect.Flags().IntP("count", "c", 0, "")
	image.AddCommand(inspect)
	create := &cobra.Command{Use: "create <name> --image <image>", Args: cobra.ExactArgs(1)}
	create.Flags().String("image", "", "")
	root.AddCommand(image, create)
	return root
}

func skillWith(body string) string {
	return "---\nname: voom\ndescription: test skill\n---\n" + body + "\n"
}

func skillWithDescription(description string) string {
	return "---\nname: voom\ndescription: " + description + "\n---\nbody\n"
}
