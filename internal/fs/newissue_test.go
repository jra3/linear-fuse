package fs

import (
	"context"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
)

func TestPriorityValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		expected int
	}{
		{"urgent", 1},
		{"high", 2},
		{"medium", 3},
		{"low", 4},
		{"none", 0},
		{"", 0},
		{"invalid", 0},
		{"URGENT", 0}, // case-sensitive
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := priorityValue(tt.name)
			if got != tt.expected {
				t.Errorf("priorityValue(%q) = %d, want %d", tt.name, got, tt.expected)
			}
		})
	}
}

func TestNewIssueNode_parseContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		title        string
		content      string
		wantTitle    string
		wantDesc     string
		wantPriority int
	}{
		{
			name:      "empty content uses filename title",
			title:     "My Issue",
			content:   "",
			wantTitle: "My Issue",
		},
		{
			name:      "plain text becomes description with filename title",
			title:     "Bug Fix",
			content:   "This is a description without frontmatter",
			wantTitle: "Bug Fix",
			wantDesc:  "This is a description without frontmatter",
		},
		{
			name:      "frontmatter with title",
			title:     "Filename Title",
			content:   "---\ntitle: Frontmatter Title\n---\nBody content",
			wantTitle: "Frontmatter Title",
			wantDesc:  "Body content",
		},
		{
			name:      "frontmatter without title uses filename",
			title:     "Filename Title",
			content:   "---\npriority: high\n---\nBody here",
			wantTitle: "Filename Title",
			wantDesc:  "Body here",
		},
		{
			name:         "frontmatter with priority",
			title:        "Issue",
			content:      "---\ntitle: Prioritized\npriority: urgent\n---\n",
			wantTitle:    "Prioritized",
			wantPriority: 1,
		},
		{
			name:      "frontmatter with empty title uses filename",
			title:     "Filename",
			content:   "---\ntitle: \"\"\n---\nDescription",
			wantTitle: "Filename",
			wantDesc:  "Description",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &NewIssueNode{
				title:   tt.title,
				content: []byte(tt.content),
			}

			input, err := node.parseContent()
			if err != nil {
				t.Fatalf("parseContent() error: %v", err)
			}

			if title, ok := input["title"].(string); !ok || title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}

			if tt.wantDesc != "" {
				if desc, ok := input["description"].(string); !ok || desc != tt.wantDesc {
					t.Errorf("description = %q, want %q", desc, tt.wantDesc)
				}
			}

			if tt.wantPriority != 0 {
				if priority, ok := input["priority"].(int); !ok || priority != tt.wantPriority {
					t.Errorf("priority = %v, want %d", input["priority"], tt.wantPriority)
				}
			}
		})
	}
}

func TestNewIssueNode_Getattr(t *testing.T) {
	t.Parallel()
	node := &NewIssueNode{
		content: []byte("test content"),
	}

	ctx := context.Background()
	out := &fuse.AttrOut{}

	errno := node.Getattr(ctx, nil, out)
	if errno != 0 {
		t.Errorf("Getattr() errno = %d, want 0", errno)
	}

	if out.Mode != 0644 {
		t.Errorf("Mode = %o, want 0644", out.Mode)
	}

	if out.Size != uint64(len("test content")) {
		t.Errorf("Size = %d, want %d", out.Size, len("test content"))
	}
}

func TestNewIssueNode_Write(t *testing.T) {
	t.Parallel()
	node := &NewIssueNode{
		lfs:     &LinearFS{},
		content: []byte{},
	}

	ctx := context.Background()
	data := []byte("hello world")

	written, errno := node.Write(ctx, nil, data, 0)
	if errno != 0 {
		t.Errorf("Write() errno = %d, want 0", errno)
	}

	if written != uint32(len(data)) {
		t.Errorf("written = %d, want %d", written, len(data))
	}

	if string(node.content) != "hello world" {
		t.Errorf("content = %q, want %q", string(node.content), "hello world")
	}
}

func TestNewIssueNode_WriteOffset(t *testing.T) {
	t.Parallel()
	node := &NewIssueNode{
		lfs:     &LinearFS{},
		content: []byte("hello"),
	}

	ctx := context.Background()

	// Write at offset 5 (append)
	written, errno := node.Write(ctx, nil, []byte(" world"), 5)
	if errno != 0 {
		t.Errorf("Write() errno = %d, want 0", errno)
	}

	if written != 6 {
		t.Errorf("written = %d, want 6", written)
	}

	if string(node.content) != "hello world" {
		t.Errorf("content = %q, want %q", string(node.content), "hello world")
	}
}

func TestNewIssueNode_Setattr_Truncate(t *testing.T) {
	t.Parallel()
	node := &NewIssueNode{
		content: []byte("hello world"),
	}

	ctx := context.Background()
	in := &fuse.SetAttrIn{}
	in.Valid = fuse.FATTR_SIZE
	in.Size = 5
	out := &fuse.AttrOut{}

	errno := node.Setattr(ctx, nil, in, out)
	if errno != 0 {
		t.Errorf("Setattr() errno = %d, want 0", errno)
	}

	if string(node.content) != "hello" {
		t.Errorf("content = %q, want %q", string(node.content), "hello")
	}

	if out.Size != 5 {
		t.Errorf("out.Size = %d, want 5", out.Size)
	}
}

func TestNewIssueNode_Setattr_Extend(t *testing.T) {
	t.Parallel()
	node := &NewIssueNode{
		content: []byte("hi"),
	}

	ctx := context.Background()
	in := &fuse.SetAttrIn{}
	in.Valid = fuse.FATTR_SIZE
	in.Size = 10
	out := &fuse.AttrOut{}

	errno := node.Setattr(ctx, nil, in, out)
	if errno != 0 {
		t.Errorf("Setattr() errno = %d, want 0", errno)
	}

	if len(node.content) != 10 {
		t.Errorf("len(content) = %d, want 10", len(node.content))
	}

	// Original content should be preserved
	if string(node.content[:2]) != "hi" {
		t.Errorf("content prefix = %q, want %q", string(node.content[:2]), "hi")
	}
}

func TestNewIssueNode_Read(t *testing.T) {
	t.Parallel()
	node := &NewIssueNode{
		content: []byte("hello world"),
	}

	ctx := context.Background()
	dest := make([]byte, 5)

	result, errno := node.Read(ctx, nil, dest, 0)
	if errno != 0 {
		t.Errorf("Read() errno = %d, want 0", errno)
	}

	data, _ := result.Bytes(nil)
	if string(data) != "hello" {
		t.Errorf("read data = %q, want %q", string(data), "hello")
	}
}

func TestNewIssueNode_ReadOffset(t *testing.T) {
	t.Parallel()
	node := &NewIssueNode{
		content: []byte("hello world"),
	}

	ctx := context.Background()
	dest := make([]byte, 5)

	result, errno := node.Read(ctx, nil, dest, 6)
	if errno != 0 {
		t.Errorf("Read() errno = %d, want 0", errno)
	}

	data, _ := result.Bytes(nil)
	if string(data) != "world" {
		t.Errorf("read data = %q, want %q", string(data), "world")
	}
}

func TestNewIssueNode_ReadBeyondEnd(t *testing.T) {
	t.Parallel()
	node := &NewIssueNode{
		content: []byte("hi"),
	}

	ctx := context.Background()
	dest := make([]byte, 10)

	result, errno := node.Read(ctx, nil, dest, 100)
	if errno != 0 {
		t.Errorf("Read() errno = %d, want 0", errno)
	}

	data, _ := result.Bytes(nil)
	if len(data) != 0 {
		t.Errorf("read beyond end should return empty, got %d bytes", len(data))
	}
}

func TestNewIssueNode_FlushEmpty(t *testing.T) {
	t.Parallel()
	node := &NewIssueNode{
		lfs:     &LinearFS{},
		content: []byte{},
	}

	ctx := context.Background()
	errno := node.Flush(ctx, nil)
	if errno != 0 {
		t.Errorf("Flush() with empty content errno = %d, want 0", errno)
	}

	// Should not mark as created
	if node.created {
		t.Error("node should not be marked created with empty content")
	}
}

func TestNewIssueNode_FlushAlreadyCreated(t *testing.T) {
	t.Parallel()
	node := &NewIssueNode{
		lfs:     &LinearFS{},
		content: []byte("some content"),
		created: true,
	}

	ctx := context.Background()
	errno := node.Flush(ctx, nil)
	if errno != 0 {
		t.Errorf("Flush() already created errno = %d, want 0", errno)
	}
}

func TestNewIssueNode_Open(t *testing.T) {
	t.Parallel()
	node := &NewIssueNode{}

	ctx := context.Background()
	_, flags, errno := node.Open(ctx, 0)
	if errno != 0 {
		t.Errorf("Open() errno = %d, want 0", errno)
	}

	if flags != fuse.FOPEN_DIRECT_IO {
		t.Errorf("flags = %d, want FOPEN_DIRECT_IO", flags)
	}
}
