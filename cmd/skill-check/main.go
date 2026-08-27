// Command skill-check validates the embedded Voom agent skill.
package main

import (
	"log"

	"github.com/mjrusso/voom/internal/cli"
	"github.com/mjrusso/voom/internal/skill"
	"github.com/mjrusso/voom/internal/skillcheck"
)

func main() {
	if err := skillcheck.Validate(skill.Markdown(), cli.NewRootCommand()); err != nil {
		log.Fatal(err)
	}
}
