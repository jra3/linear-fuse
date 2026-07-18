package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/db"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show mount, sync, cache, and budget status",
	Long: `Report a health snapshot of the local LinearFS state: mount liveness,
the SQLite cache (workspace size, last full sync, pending detail-sync backlog),
and — when the JSONL metrics export is on — the current rate-limit budget.

It reads the local cache and config read-only and does NOT talk to the daemon,
so it works whether or not the service is running. Live in-memory budget lives
in the daemon; the values shown come from the last metrics snapshot on disk.`,
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()

	configPath, _ := cmd.Flags().GetString("config")
	var (
		cfg    *config.Config
		cfgErr error
	)
	if configPath != "" {
		cfg, cfgErr = config.LoadFrom(configPath)
	} else {
		cfg, cfgErr = config.Load()
	}
	if cfgErr != nil {
		// A broken config file shouldn't blind the whole command; fall back to
		// defaults and note it.
		cfg = config.DefaultConfig()
	}

	fmt.Fprintf(out, "linearfs %s (%s), built %s, %s %s/%s\n\n",
		Version, GitCommit, BuildDate, runtime.Version(), runtime.GOOS, runtime.GOARCH)

	// --- Config ---
	fmt.Fprintln(out, "Config:")
	if cfgErr != nil {
		fmt.Fprintf(out, "  file:      ERROR (%v) — using defaults\n", cfgErr)
	} else if configPath != "" {
		fmt.Fprintf(out, "  file:      %s\n", configPath)
	} else {
		fmt.Fprintf(out, "  file:      %s\n", defaultConfigPath())
	}
	fmt.Fprintf(out, "  api key:   %s\n", apiKeySource(cfg))

	// --- Mount ---
	fmt.Fprintln(out, "\nMount:")
	reportMounts(out, cfg.Mount.DefaultPath)

	// --- Cache (SQLite) ---
	dbPath := db.DefaultDBPath()
	fmt.Fprintln(out, "\nCache:")
	fmt.Fprintf(out, "  db:        %s\n", dbPath)
	reportCache(out, dbPath)

	// --- Budget (from the metrics export, if present) ---
	fmt.Fprintln(out, "\nBudget:")
	reportBudget(out, cfg.Telemetry.File.Path)

	return nil
}

// defaultConfigPath mirrors config.LoadWithEnv's default location
// (os.UserConfigDir honors XDG_CONFIG_HOME on Linux, falling back to ~/.config).
func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "linearfs", "config.yaml")
}

func apiKeySource(cfg *config.Config) string {
	if os.Getenv("LINEAR_API_KEY") != "" {
		return "set (LINEAR_API_KEY env)"
	}
	if cfg != nil && cfg.APIKey != "" {
		return "set (config file)"
	}
	return "NOT SET"
}

// reportMounts lists the active linearfs mounts (auto-detected from
// /proc/self/mounts, so it finds the real mountpoint however the daemon was
// started — CLI arg or config) plus the configured default if it isn't already
// among them. Each is probed with statfs to catch a wedged mount.
func reportMounts(out io.Writer, configured string) {
	active := detectLinearfsMounts()
	if len(active) == 0 && configured == "" {
		fmt.Fprintln(out, "  no active linearfs mount (and no default_path configured)")
		return
	}
	seen := map[string]bool{}
	for _, mp := range active {
		fmt.Fprintf(out, "  %s  [%s]\n", mp, liveOrWedged(mp))
		seen[mp] = true
	}
	if configured != "" {
		if abs, err := filepath.Abs(configured); err == nil {
			configured = abs
		}
		if !seen[configured] {
			fmt.Fprintf(out, "  %s  [%s]  (configured default)\n", configured, liveOrWedged(configured))
		}
	}
}

// detectLinearfsMounts returns the mountpoints whose filesystem type is
// fuse.linearfs (Linux). Empty on platforms without /proc/self/mounts (macOS),
// where the caller falls back to the configured path.
func detectLinearfsMounts() []string {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && strings.HasPrefix(fields[2], "fuse.linearfs") {
			out = append(out, unescapeMountField(fields[1]))
		}
	}
	return out
}

// liveOrWedged reports "live", a wedged-recovery hint, or a not-mounted note by
// probing the mountpoint. Read-only: it never unmounts (that is `mount`'s
// preflight job).
func liveOrWedged(mp string) string {
	var st syscall.Statfs_t
	if err := syscall.Statfs(mp, &st); err != nil {
		if errors.Is(err, syscall.ENOTCONN) {
			return "WEDGED — recover with: fusermount3 -uz " + mp
		}
		if errors.Is(err, os.ErrNotExist) {
			return "not mounted (path does not exist)"
		}
		return "unknown: " + err.Error()
	}
	for _, m := range detectLinearfsMounts() {
		if m == mp {
			return "live"
		}
	}
	return "not mounted (plain directory)"
}

// unescapeMountField decodes the octal escapes the kernel writes into
// /proc/self/mounts fields (space = \040, tab = \011, newline = \012, \ = \134).
func unescapeMountField(s string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(s)
}

func reportCache(out io.Writer, dbPath string) {
	info, err := os.Stat(dbPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(out, "  state:     not created yet (the daemon has not run)")
		} else {
			fmt.Fprintf(out, "  state:     unreadable (%v)\n", err)
		}
		return
	}
	fmt.Fprintf(out, "  size:      %s\n", humanBytes(fileSetSize(dbPath, info)))

	// A normal read/write connection that only issues SELECTs — this coexists
	// with the daemon's WAL connection (a read-only open of a live WAL database
	// is fussier). No schema init or migration: status must not mutate.
	escaped := strings.ReplaceAll(dbPath, " ", "%20")
	conn, err := sql.Open("sqlite", "file:"+escaped+"?_time_format=sqlite&_pragma=busy_timeout(3000)")
	if err != nil {
		fmt.Fprintf(out, "  state:     could not open (%v)\n", err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if teams, err := scalarCount(ctx, conn, "SELECT COUNT(*) FROM teams"); err == nil {
		fmt.Fprintf(out, "  teams:     %d\n", teams)
	}
	if issues, err := scalarCount(ctx, conn, "SELECT COUNT(*) FROM issues"); err == nil {
		fmt.Fprintf(out, "  issues:    %d\n", issues)
	}

	q := db.New(conn)
	if last, err := q.GetSyncSchedule(ctx, "full_cycle"); err == nil {
		fmt.Fprintf(out, "  last full sync: %s (%s)\n",
			humanAgo(time.Since(last)), last.Local().Format("2006-01-02 15:04"))
	} else if errors.Is(err, sql.ErrNoRows) {
		fmt.Fprintln(out, "  last full sync: never (first sync pending)")
	}
	if pending, err := q.CountPendingDetailSync(ctx); err == nil {
		suffix := ""
		if pending > 0 {
			suffix = " (awaiting budget/next cycle)"
		}
		fmt.Fprintf(out, "  pending detail sync: %d issues%s\n", pending, suffix)
	}
}

func scalarCount(ctx context.Context, conn *sql.DB, query string) (int64, error) {
	var n int64
	err := conn.QueryRowContext(ctx, query).Scan(&n)
	return n, err
}

// fileSetSize sums the db file and its -wal/-shm siblings (the WAL can hold a
// meaningful fraction of the live data).
func fileSetSize(dbPath string, info os.FileInfo) int64 {
	total := info.Size()
	for _, suffix := range []string{"-wal", "-shm"} {
		if si, err := os.Stat(dbPath + suffix); err == nil {
			total += si.Size()
		}
	}
	return total
}

// reportBudget renders the most recent rate-limit budget snapshot from the
// JSONL metrics export. The live budget lives in the daemon's memory; this is
// the on-disk rendering (the always-on journald summary is the other one).
func reportBudget(out io.Writer, metricsPath string) {
	if metricsPath == "" {
		metricsPath = config.DefaultTelemetryPath()
	}
	budget, at, err := latestBudget(metricsPath)
	if err != nil {
		fmt.Fprintf(out, "  unavailable — enable telemetry.file, or read the journald summary\n")
		fmt.Fprintf(out, "               (journalctl --user -u linearfs | grep budget)\n")
		return
	}
	fmt.Fprintf(out, "  (snapshot %s from %s)\n", humanAgo(time.Since(at)), metricsPath)
	for _, axis := range []string{"requests", "complexity"} {
		m := budget[axis]
		if m == nil {
			continue
		}
		limit, remaining := m["limit"], m["remaining"]
		usedPct := 0.0
		if limit > 0 {
			usedPct = (limit - remaining) / limit * 100
		}
		line := fmt.Sprintf("  %-11s %s / %s used (%.1f%%)",
			axis+":", humanNum(limit-remaining), humanNum(limit), usedPct)
		if reset, ok := m["reset_seconds"]; ok && reset > 0 {
			line += fmt.Sprintf(", resets in %s", humanAgo(time.Duration(reset)*time.Second))
		}
		if inflight, ok := m["inflight"]; ok && inflight > 0 {
			line += fmt.Sprintf(", %s in flight", humanNum(inflight))
		}
		fmt.Fprintln(out, line)
	}
}

// budgetByAxis maps axis -> {limit,remaining,reset_seconds,inflight}.
type budgetByAxis map[string]map[string]float64

func latestBudget(path string) (budgetByAxis, time.Time, error) {
	line, err := lastLine(path, 512*1024)
	if err != nil {
		return nil, time.Time{}, err
	}
	var rec struct {
		ScopeMetrics []struct {
			Metrics []struct {
				Name string
				Data struct {
					DataPoints []struct {
						Attributes []struct {
							Key   string
							Value struct{ Value any }
						}
						Time  time.Time
						Value any
					}
				}
			}
		}
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, time.Time{}, err
	}
	out := budgetByAxis{}
	var stamp time.Time
	for _, sm := range rec.ScopeMetrics {
		for _, m := range sm.Metrics {
			short := strings.TrimPrefix(m.Name, "linearfs.budget.")
			if short == m.Name {
				continue
			}
			for _, dp := range m.Data.DataPoints {
				axis := ""
				for _, a := range dp.Attributes {
					if a.Key == "axis" {
						axis = fmt.Sprint(a.Value.Value)
					}
				}
				if axis == "" {
					continue
				}
				if out[axis] == nil {
					out[axis] = map[string]float64{}
				}
				out[axis][short] = toFloat(dp.Value)
				if dp.Time.After(stamp) {
					stamp = dp.Time
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, time.Time{}, errors.New("no budget metrics in snapshot")
	}
	return out, stamp, nil
}

// lastLine returns the final non-empty line of a file, reading at most maxBytes
// from the end (the metrics export is append-only JSONL and can be large).
func lastLine(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size == 0 {
		return nil, errors.New("empty metrics file")
	}
	start := int64(0)
	if size > maxBytes {
		start = size - maxBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimRight(string(buf), "\n\r \t")
	if i := strings.LastIndexByte(trimmed, '\n'); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	return []byte(trimmed), nil
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

func humanAgo(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func humanNum(f float64) string {
	n := int64(f)
	s := fmt.Sprintf("%d", n)
	// thousands separators
	var b strings.Builder
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
		b.WriteByte('-')
	}
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
