package api

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// Linear's rate limit
	linearHourlyLimit = 1500

	// How often to log stats (when enabled)
	statsLogInterval = 5 * time.Minute

	// How long to keep call timestamps for rolling window
	rollingWindowDuration = time.Hour
)

// OperationStats tracks metrics for a single GraphQL operation.
type OperationStats struct {
	Count       int64 // total calls
	TotalTimeNs int64 // for computing avg latency
	Errors      int64 // failed calls
}

// APIStats tracks GraphQL API call statistics.
type APIStats struct {
	mu              sync.RWMutex
	operations      map[string]*OperationStats
	recentCalls     []time.Time // timestamps for rolling hourly window
	rateLimitWaitNs int64       // total time waiting for rate limiter (atomic)
	startTime       time.Time
	stopCh          chan struct{}
	wg              sync.WaitGroup
}

// NewAPIStats creates a new API stats tracker.
// The periodic stats logger always runs at 5-minute intervals for observability.
// The logEnabled parameter and LINEARFS_API_STATS env var are kept for compatibility but no longer needed.
func NewAPIStats(logEnabled bool) *APIStats {
	s := &APIStats{
		operations:  make(map[string]*OperationStats),
		recentCalls: make([]time.Time, 0, 1000),
		startTime:   time.Now(),
		stopCh:      make(chan struct{}),
	}

	// Always run periodic logger — the 5-minute interval is not noisy
	// and provides essential observability for rate limit issues.
	s.wg.Add(1)
	go s.periodicLogger()

	return s
}

// Record records an API call with its operation name, duration, and any error.
// This method is safe for concurrent use.
func (s *APIStats) Record(opName string, duration time.Duration, err error) {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Get or create operation stats
	stats, ok := s.operations[opName]
	if !ok {
		stats = &OperationStats{}
		s.operations[opName] = stats
	}

	// Update counters
	stats.Count++
	stats.TotalTimeNs += duration.Nanoseconds()
	if err != nil {
		stats.Errors++
	}

	// Add to rolling window
	s.recentCalls = append(s.recentCalls, now)

	// Cleanup old timestamps (older than 1 hour)
	cutoff := now.Add(-rollingWindowDuration)
	firstValid := 0
	for i, t := range s.recentCalls {
		if t.After(cutoff) {
			firstValid = i
			break
		}
	}
	if firstValid > 0 {
		// Shift slice to remove old entries
		s.recentCalls = s.recentCalls[firstValid:]
	}
}

// RecordRateLimitWait records time spent waiting for the rate limiter.
func (s *APIStats) RecordRateLimitWait(duration time.Duration) {
	atomic.AddInt64(&s.rateLimitWaitNs, duration.Nanoseconds())
}

// HourlyCount returns the number of API calls in the last hour.
func (s *APIStats) HourlyCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cutoff := time.Now().Add(-rollingWindowDuration)
	count := 0
	for _, t := range s.recentCalls {
		if t.After(cutoff) {
			count++
		}
	}
	return count
}

// HourlyRate returns the percentage of Linear's hourly limit used.
func (s *APIStats) HourlyRate() float64 {
	return float64(s.HourlyCount()) / float64(linearHourlyLimit) * 100
}

// RateLimitWaitTotal returns the total time spent waiting for the rate limiter.
func (s *APIStats) RateLimitWaitTotal() time.Duration {
	return time.Duration(atomic.LoadInt64(&s.rateLimitWaitNs))
}

// BudgetSnapshot returns the current hourly call count and percentage used.
func (s *APIStats) BudgetSnapshot() (count int, pct float64) {
	count = s.HourlyCount()
	pct = float64(count) / float64(linearHourlyLimit) * 100
	return
}

// Summary returns a formatted summary of API stats.
func (s *APIStats) Summary() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Count calls in last 5 minutes
	now := time.Now()
	fiveMinAgo := now.Add(-5 * time.Minute)
	recentCount := 0
	for _, t := range s.recentCalls {
		if t.After(fiveMinAgo) {
			recentCount++
		}
	}

	hourlyCount := 0
	hourAgo := now.Add(-rollingWindowDuration)
	for _, t := range s.recentCalls {
		if t.After(hourAgo) {
			hourlyCount++
		}
	}

	hourlyRate := float64(hourlyCount) / float64(linearHourlyLimit) * 100
	rateLimitWait := time.Duration(atomic.LoadInt64(&s.rateLimitWaitNs))

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[API-STATS] 5min: %d calls | %d/hr (%.1f%% of limit)",
		recentCount, hourlyCount, hourlyRate))

	if rateLimitWait > 0 {
		sb.WriteString(fmt.Sprintf(" | rate-wait: %s", formatDuration(rateLimitWait)))
	}
	sb.WriteString("\n")

	// Sort operations by count (descending)
	type opEntry struct {
		name  string
		stats *OperationStats
	}
	ops := make([]opEntry, 0, len(s.operations))
	for name, stats := range s.operations {
		ops = append(ops, opEntry{name, stats})
	}
	sort.Slice(ops, func(i, j int) bool {
		return ops[i].stats.Count > ops[j].stats.Count
	})

	// Format each operation
	for _, op := range ops {
		avgMs := float64(op.stats.TotalTimeNs) / float64(op.stats.Count) / 1e6
		line := fmt.Sprintf("  %-25s %4d  avg:%s",
			op.name, op.stats.Count, formatMillis(avgMs))
		if op.stats.Errors > 0 {
			line += fmt.Sprintf("  errors:%d", op.stats.Errors)
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return sb.String()
}

// Close stops the periodic logger and waits for it to finish.
func (s *APIStats) Close() {
	close(s.stopCh)
	s.wg.Wait()
}

// periodicLogger logs stats every statsLogInterval.
func (s *APIStats) periodicLogger() {
	defer s.wg.Done()

	ticker := time.NewTicker(statsLogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			summary := s.Summary()
			log.Print(summary)
		case <-s.stopCh:
			// Log final stats on shutdown
			summary := s.Summary()
			log.Print("[API-STATS] Final stats:\n" + summary)
			return
		}
	}
}

// formatDuration formats a duration for display (e.g., "1.2s", "450ms").
func formatDuration(d time.Duration) string {
	if d >= time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// formatMillis formats milliseconds for display.
func formatMillis(ms float64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", ms/1000)
	}
	return fmt.Sprintf("%.0fms", ms)
}

// extractOpName extracts the GraphQL operation name from a query string.
// It finds the first word before '{' or '(' after "query" or "mutation".
func extractOpName(query string) string {
	if len(query) == 0 {
		return "unknown"
	}

	// Simple extraction: find first { or ( and take word before it
	for i, ch := range query {
		if ch == '{' || ch == '(' {
			if i == 0 {
				return "unknown"
			}
			// Walk backwards to find operation name
			end := i - 1
			for end > 0 && (query[end] == ' ' || query[end] == '\n') {
				end--
			}
			if end < 0 {
				return "unknown"
			}
			start := end
			for start > 0 && query[start-1] != ' ' && query[start-1] != '\n' {
				start--
			}
			if start <= end && end >= 0 {
				name := query[start : end+1]
				// Skip "query" or "mutation" keywords
				if name == "query" || name == "mutation" {
					return "unknown"
				}
				return name
			}
			break
		}
	}
	return "unknown"
}
