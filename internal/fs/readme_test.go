package fs

import (
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/config"
)

// TestGenerateReadmeFeedbackFlag pins both states of the USER_FEEDBACK flag:
// off is byte-for-byte the plain README (the default every normal user gets),
// on appends the self-reporting protocol and changes nothing else.
func TestGenerateReadmeFeedbackFlag(t *testing.T) {
	t.Parallel()
	const mount = "/tmp/linear"

	off := generateReadme(mount, false)
	on := generateReadme(mount, true)

	for _, marker := range []string{"agent_feedback_protocol", "dx-friction", "gh issue create", "USER_FEEDBACK"} {
		if strings.Contains(off, marker) {
			t.Errorf("flag-off README mentions %q; the protocol must be entirely off-path", marker)
		}
		if !strings.Contains(on, marker) {
			t.Errorf("flag-on README does not mention %q", marker)
		}
	}

	// Append-only: the flag-on README is the flag-off one plus the section.
	if !strings.HasPrefix(on, off) {
		t.Error("flag-on README is not the flag-off README plus an appended section")
	}
	if strings.TrimPrefix(on, off) != agentFeedbackProtocol {
		t.Error("flag-on README appends something other than agentFeedbackProtocol")
	}

	// The protocol has to carry the pieces that make it actionable: the target
	// repo, the dedup search, the receipt, and the house-rule carve-out.
	for _, want := range []string{
		"jra3/linear-fuse",
		"gh issue list --repo jra3/linear-fuse --search",
		"gh issue comment",
		"filed linear-fuse bug #N",
		"overrides any",
	} {
		if !strings.Contains(on, want) {
			t.Errorf("feedback protocol does not mention %q", want)
		}
	}
}

// TestNewLinearFSCarriesFeedbackFlag pins the config → filesystem wiring: the
// README generator reads lfs.userFeedback, so a set flag has to survive
// construction (and an unset one must not turn itself on).
func TestNewLinearFSCarriesFeedbackFlag(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		want bool
	}{{"off", false}, {"on", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lfs, err := NewLinearFS(&config.Config{APIKey: "test-key", UserFeedback: tc.want}, false)
			if err != nil {
				t.Fatalf("NewLinearFS failed: %v", err)
			}
			defer lfs.Close()
			if lfs.userFeedback != tc.want {
				t.Errorf("lfs.userFeedback = %v, want %v", lfs.userFeedback, tc.want)
			}
		})
	}
}
