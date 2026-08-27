package cli

import (
	"encoding/json"
	"io"

	"github.com/spf13/cobra"

	voomskill "github.com/mjrusso/voom/internal/skill"
)

type skillInfo struct {
	Name            string `json:"name"`
	ProducerVersion string `json:"producerVersion"`
	ContentSHA256   string `json:"contentSha256"`
	Content         string `json:"content"`
}

func writeSkill(w io.Writer, producerVersion, format string) error {
	if format == "json" {
		return json.NewEncoder(w).Encode(skillInfo{
			Name:            "voom",
			ProducerVersion: producerVersion,
			ContentSHA256:   voomskill.SHA256(),
			Content:         voomskill.Markdown(),
		})
	}
	_, err := io.WriteString(w, voomskill.Markdown())
	return err
}

func newSkillCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "skill",
		Short: "Print the Voom agent skill",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return writeSkill(cmd.OutOrStdout(), CurrentVersion().Version, outputFormat(cmd))
		},
	}
}
