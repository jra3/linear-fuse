package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"time"

	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/fs"
	"github.com/jra3/linear-fuse/internal/telemetry"
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
	// --config names an exact file (unreadable = error); without it the
	// default XDG path applies (missing = defaults + env).
	var cfg *config.Config
	var err error
	if configPath, _ := cmd.Flags().GetString("config"); configPath != "" {
		cfg, err = config.LoadFrom(configPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	mountpoint := cfg.Mount.DefaultPath
	if len(args) > 0 {
		mountpoint = args[0]
	}

	if strings.HasPrefix(mountpoint, "~/") {
		home, _ := os.UserHomeDir()
		mountpoint = filepath.Join(home, mountpoint[2:])
	}

	if mountpoint == "" {
		return fmt.Errorf("mountpoint required: linearfs mount /path/to/mount")
	}

	// Preflight the mountpoint before touching it. Heals the wedged-mount
	// incident (a dead FUSE mount — "Transport endpoint is not connected" —
	// left by a crash made mkdir fail and sent systemd into a restart loop);
	// refuses a healthy live mount rather than kill a concurrent instance.
	if err := fs.PreflightMountpoint(mountpoint); err != nil {
		return fmt.Errorf("mountpoint preflight: %w", err)
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

	// Telemetry first, so instruments registered during filesystem/worker
	// construction land on the real provider. Failure must never block
	// mounting — log and continue without it.
	//
	// flushTelemetry is idempotent (sync.Once): called explicitly after
	// server.Wait() — BEFORE lfs.Close(), because the final export's
	// observable callbacks (e.g. sync.pending_depth) read the store — and
	// kept as a defer so early error returns still flush.
	flushTelemetry := func() {}
	if shutdownTelemetry, err := telemetry.Init(cfg.Telemetry, Version, GitCommit); err != nil {
		fmt.Printf("Warning: telemetry disabled: %v\n", err)
	} else {
		var once sync.Once
		flushTelemetry = func() {
			once.Do(func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := shutdownTelemetry(shutdownCtx); err != nil {
					fmt.Printf("Warning: telemetry shutdown failed: %v\n", err)
				}
			})
		}
		defer flushTelemetry()
	}

	// Create LinearFS instance
	lfs, err := fs.NewLinearFS(cfg, debug)
	if err != nil {
		return fmt.Errorf("failed to create filesystem: %w", err)
	}

	// Enable SQLite persistent cache and background sync BEFORE mounting
	// This must complete before the filesystem is accessible to prevent nil repo panics
	if err := lfs.EnableSQLiteCache(""); err != nil {
		fmt.Printf("Warning: SQLite cache disabled: %v\n", err)
	}

	// Now mount the filesystem
	server, err := fs.MountFS(mountpoint, lfs, debug)
	if err != nil {
		lfs.Close()
		return fmt.Errorf("failed to mount: %w", err)
	}

	// Handle signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nUnmounting...")
		_ = server.Unmount()
	}()

	fmt.Println("Filesystem mounted. Press Ctrl+C to unmount.")
	server.Wait()

	// Shutdown ordering matters: flush telemetry while the store is still
	// open (the final export's observable callbacks collect from it), THEN
	// stop background goroutines and close the store.
	flushTelemetry()
	lfs.Close()

	return nil
}
