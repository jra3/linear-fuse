package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/fs"
	"github.com/spf13/cobra"
)

var mountCmd = &cobra.Command{
	Use:   "mount [mountpoint]",
	Short: "Mount the Linear filesystem",
	Long:  `Mount your Linear workspace at the specified mountpoint.`,
	Args:  cobra.MaximumNArgs(1),
	RunE:  runMount,
}

func init() {
	rootCmd.AddCommand(mountCmd)
	mountCmd.Flags().BoolP("foreground", "f", false, "run in foreground (don't daemonize)")
}

func runMount(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	mountpoint := cfg.Mount.DefaultPath
	if len(args) > 0 {
		mountpoint = args[0]
	}

	if mountpoint == "" {
		return fmt.Errorf("mountpoint required: linearfs mount /path/to/mount")
	}

	// Ensure mountpoint exists
	if err := os.MkdirAll(mountpoint, 0755); err != nil {
		return fmt.Errorf("failed to create mountpoint: %w", err)
	}

	debug, _ := cmd.Flags().GetBool("debug")
	if d, _ := cmd.Root().PersistentFlags().GetBool("debug"); d {
		debug = true
	}

	fmt.Printf("Mounting Linear filesystem at %s\n", mountpoint)

	server, lfs, err := fs.Mount(mountpoint, cfg, debug)
	if err != nil {
		return fmt.Errorf("failed to mount: %w", err)
	}

	// Enable SQLite persistent cache and background sync
	ctx := context.Background()
	if err := lfs.EnableSQLiteCache(ctx, ""); err != nil {
		fmt.Printf("Warning: SQLite cache disabled: %v\n", err)
	}

	// Handle signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nUnmounting...")
		server.Unmount()
	}()

	fmt.Println("Filesystem mounted. Press Ctrl+C to unmount.")
	server.Wait()

	// Stop background cache goroutines
	lfs.Close()

	return nil
}
