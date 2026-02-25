package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sausheong/goclaw/internal/llm"
)

// ReadFileTool reads the contents of a file.
type ReadFileTool struct{}

type readFileInput struct {
	Path string `json:"path"`
}

func (t *ReadFileTool) Name() string { return "read_file" }

func (t *ReadFileTool) Description() string {
	return "Read the contents of a file at the given path. Returns the file contents as text. For image files (jpg, png, gif, webp, bmp), returns the image for visual inspection."
}

func (t *ReadFileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "The absolute or relative path to the file to read"
			}
		},
		"required": ["path"]
	}`)
}

// imageExtMap maps file extensions to MIME types for image files.
var imageExtMap = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
}

func (t *ReadFileTool) Execute(_ context.Context, input json.RawMessage) (ToolResult, error) {
	var in readFileInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}

	if in.Path == "" {
		return ToolResult{Error: "path is required"}, nil
	}

	data, err := os.ReadFile(in.Path)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to read file: %v", err)}, nil
	}

	// Check if this is an image file
	ext := strings.ToLower(filepath.Ext(in.Path))
	if mimeType, ok := imageExtMap[ext]; ok {
		return ToolResult{
			Output: fmt.Sprintf("Image file: %s (%d bytes)", filepath.Base(in.Path), len(data)),
			Images: []llm.ImageContent{
				{MimeType: mimeType, Data: data},
			},
		}, nil
	}

	return ToolResult{Output: string(data)}, nil
}
