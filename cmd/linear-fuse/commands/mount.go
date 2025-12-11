package commands

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/jra3/linear-fuse/pkg/fuse"
	"github.com/jra3/linear-fuse/pkg/linear"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var mountCmd = &cobra.Command{
	Use:   "mount [mountpoint]",
	Short: "Mount Linear issues as a filesystem",
	Long: `Mount Linear.app issues as a FUSE filesystem at the specified mountpoint.
Each issue will be represented as a markdown file with YAML frontmatter.`,
	Args: cobra.ExactArgs(1),
	RunE: runMount,
}

func init() {
	rootCmd.AddCommand(mountCmd)
	mountCmd.Flags().Bool("debug", false, "Enable debug logging")
	viper.BindPFlag("debug", mountCmd.Flags().Lookup("debug"))
}

func runMount(cmd *cobra.Command, args []string) error {
	mountpoint := args[0]

	apiKey := viper.GetString("api-key")
	if apiKey == "" {
		return fmt.Errorf("API key is required. Set via --api-key flag, LINEAR_API_KEY env var, or config file")
	}

	debug := viper.GetBool("debug")

	// Create Linear API client
	client := linear.NewClient(apiKey)

	// Create FUSE filesystem
	fs, err := fuse.NewLinearFS(client, debug)
	if err != nil {
		return fmt.Errorf("failed to create filesystem: %w", err)
	}

	// Mount the filesystem
	server, err := fs.Mount(mountpoint)
	if err != nil {
		return fmt.Errorf("failed to mount filesystem: %w", err)
	}

	log.Printf("Mounted Linear filesystem at %s", mountpoint)
	log.Printf("Press Ctrl+C to unmount")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Printf("Unmounting filesystem...")
	if err := server.Unmount(); err != nil {
		return fmt.Errorf("failed to unmount: %w", err)
	}

	log.Printf("Filesystem unmounted successfully")
	return nil
}
