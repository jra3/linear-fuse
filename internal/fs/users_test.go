package fs

import (
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

func TestUserDirName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		user api.User
		want string
	}{
		{
			name: "user with displayName",
			user: api.User{
				DisplayName: "jsmith",
				Email:       "john.smith@example.com",
			},
			want: "jsmith",
		},
		{
			name: "user without displayName uses email local part",
			user: api.User{
				DisplayName: "",
				Email:       "john.smith@example.com",
			},
			want: "john.smith",
		},
		{
			name: "user with email but no @",
			user: api.User{
				DisplayName: "",
				Email:       "localonly",
			},
			want: "localonly",
		},
		{
			name: "displayName takes precedence",
			user: api.User{
				DisplayName: "johnny",
				Email:       "john.smith@example.com",
			},
			want: "johnny",
		},
		{
			name: "empty displayName and email",
			user: api.User{
				DisplayName: "",
				Email:       "",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := userDirName(tt.user)
			if got != tt.want {
				t.Errorf("userDirName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUserInfoNode_GenerateContent(t *testing.T) {
	t.Parallel()
	node := &UserInfoNode{
		user: api.User{
			ID:          "user-123",
			Name:        "John Smith",
			Email:       "john@example.com",
			DisplayName: "jsmith",
			Active:      true,
		},
	}

	content := node.generateContent()
	contentStr := string(content)

	checks := []string{
		"id: user-123",
		"name: John Smith",
		"email: john@example.com",
		"displayName: jsmith",
		"status: active",
		"# John Smith",
	}

	for _, check := range checks {
		if !strings.Contains(contentStr, check) {
			t.Errorf("generateContent() missing %q in:\n%s", check, contentStr)
		}
	}
}

func TestUserInfoNode_GenerateContent_Inactive(t *testing.T) {
	t.Parallel()
	node := &UserInfoNode{
		user: api.User{
			ID:     "user-456",
			Name:   "Jane Doe",
			Email:  "jane@example.com",
			Active: false,
		},
	}

	content := node.generateContent()
	contentStr := string(content)

	if !strings.Contains(contentStr, "status: inactive") {
		t.Error("expected status: inactive for inactive user")
	}
}
