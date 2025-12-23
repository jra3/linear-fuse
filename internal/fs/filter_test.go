package fs

import (
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

func TestAssigneeHandle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		user *api.User
		want string
	}{
		{
			name: "nil user",
			user: nil,
			want: "",
		},
		{
			name: "user with displayName",
			user: &api.User{
				DisplayName: "jsmith",
				Email:       "john.smith@example.com",
			},
			want: "jsmith",
		},
		{
			name: "user without displayName uses email local part",
			user: &api.User{
				DisplayName: "",
				Email:       "john.smith@example.com",
			},
			want: "john.smith",
		},
		{
			name: "user with email but no @",
			user: &api.User{
				DisplayName: "",
				Email:       "localonly",
			},
			want: "localonly",
		},
		{
			name: "displayName takes precedence over email",
			user: &api.User{
				DisplayName: "johnny",
				Email:       "john.smith@example.com",
			},
			want: "johnny",
		},
		{
			name: "empty displayName and email",
			user: &api.User{
				DisplayName: "",
				Email:       "",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := assigneeHandle(tt.user)
			if got != tt.want {
				t.Errorf("assigneeHandle() = %q, want %q", got, tt.want)
			}
		})
	}
}
