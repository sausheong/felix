package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sausheong/felix/internal/llm"
)

// ReadFileTool reads the contents of a file.
type ReadFileTool struct {
	WorkDir string // if set, restricts reads to this directory
}

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

// detectImageMIMEFromBytes inspects magic bytes to determine the actual image
// format, falling back to hint (typically derived from file extension).
func detectImageMIMEFromBytes(data []byte, hint string) string {
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if len(data) >= 4 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		return "image/png"
	}
	if len(data) >= 4 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F' && data[3] == '8' {
		return "image/gif"
	}
	if len(data) >= 4 && data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' {
		return "image/webp"
	}
	return hint
}

func (t *ReadFileTool) Execute(_ context.Context, input json.RawMessage) (ToolResult, error) {
	var in readFileInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}

	if in.Path == "" {
		return ToolResult{Error: "path is required"}, nil
	}

	in.Path = expandHome(in.Path)
	in.Path = resolveExistingPath(in.Path)

	if t.WorkDir != "" {
		if err := validatePathInWorkDir(in.Path, t.WorkDir); err != nil {
			return ToolResult{Error: err.Error()}, nil
		}
	}

	data, err := os.ReadFile(in.Path)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to read file: %v", err)}, nil
	}

	// Check if this is an image file
	ext := strings.ToLower(filepath.Ext(in.Path))
	if mimeType, ok := imageExtMap[ext]; ok {
		mimeType = detectImageMIMEFromBytes(data, mimeType)
		return ToolResult{
			Output: fmt.Sprintf("Image file: %s (%d bytes)", filepath.Base(in.Path), len(data)),
			Images: []llm.ImageContent{
				{MimeType: mimeType, Data: data},
			},
		}, nil
	}

	return ToolResult{Output: string(data)}, nil
}
