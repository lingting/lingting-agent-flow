package cli

import (
	"fmt"

	"github.com/lingting/agentflow/internal/app"
	"github.com/spf13/cobra"
)

// newVersionCommand 构建 version 子命令。
func newVersionCommand(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "显示版本、提交与构建时间",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprint(a.Stdout(), a.Build().String())
			return err
		},
	}
}
