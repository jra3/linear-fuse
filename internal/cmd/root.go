package cmd

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "linearfs",
	Short: "Mount Linear as a filesystem",
	Long:  `LinearFS exposes your Linear workspace as a FUSE filesystem, allowing you to browse and edit issues as markdown files.`,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringP("config", "c", "", "config file (default: ~/.config/linearfs/config.yaml)")
	rootCmd.PersistentFlags().BoolP("debug", "d", false, "enable debug logging")
}
