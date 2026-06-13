package skill

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// openclawMetadata holds OpenClaw-compatible metadata from YAML frontmatter.
type openclawMetadata struct {
	OpenClaw struct {
		Requires struct {
			Bins []string `yaml:"bins"`
		} `yaml:"requires"`
	} `yaml:"openclaw"`
}

// Skill represents a loaded skill with YAML frontmatter metadata and Markdown body.
type Skill struct {
	// From YAML frontmatter
	Name        string           `yaml:"name"`
	Description string           `yaml:"description"`
	Tags        []string         `yaml:"tags,omitempty"`
	Metadata    openclawMetadata `yaml:"metadata,omitempty"`

	// Parsed content
	Body     string // Markdown body (after frontmatter)
	FilePath string // Source file path
}

// Loader scans directories for SKILL.md files and loads them.
type Loader struct {
	skills []Skill
	mu     sync.RWMutex
}

// NewLoader creates a new skill loader.
func NewLoader() *Loader {
	return &Loader{}
}

// LoadFrom scans directories for SKILL.md files and loads all found skills.
// It accepts multiple directories (e.g. workspace/skills/ and ~/.felix/skills/).
func (l *Loader) LoadFrom(dirs ...string) error {
	var loaded []Skill

	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}

		err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // skip errors
			}
			if d.IsDir() {
				return nil
			}
			// Accept both <dir>/SKILL.md (OpenClaw convention) and
			// any *.md file directly in the skills directory.
			name := d.Name()
			isSkillMD := strings.ToUpper(name) == "SKILL.MD"
			isDirectMD := strings.HasSuffix(strings.ToLower(name), ".md") && filepath.Dir(path) == dir
			if !isSkillMD && !isDirectMD {
				return nil
			}

			skill, err := ParseSkillFile(path)
			if err != nil {
				slog.Warn("failed to parse skill file", "path", path, "error", err)
				return nil
			}

			// Skip skills whose required binaries are not installed
			if missing := MissingBins(skill); len(missing) > 0 {
				slog.Info("skill skipped (missing binary)", "name", skill.Name, "binary", missing[0])
				return nil
			}

			loaded = append(loaded, skill)
			slog.Info("loaded skill", "name", skill.Name, "path", path)
			return nil
		})
		if err != nil {
			slog.Warn("error scanning skills directory", "dir", dir, "error", err)
		}
	}

	l.mu.Lock()
	l.skills = loaded
	l.mu.Unlock()

	return nil
}

// Skills returns all loaded skills.
func (l *Loader) Skills() []Skill {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return append([]Skill{}, l.skills...)
}

// ParseSkillFile reads a SKILL.md file and parses its frontmatter and body.
func ParseSkillFile(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}

	frontmatter, body := SplitFrontmatter(string(data))

	var skill Skill
	if frontmatter != "" {
		if err := yaml.Unmarshal([]byte(frontmatter), &skill); err != nil {
			return Skill{}, err
		}
	}

	skill.Body = strings.TrimSpace(body)
	skill.FilePath = path

	// Default name from directory name (for SKILL.md) or filename stem (for *.md)
	if skill.Name == "" {
		if strings.ToUpper(filepath.Base(path)) == "SKILL.MD" {
			skill.Name = filepath.Base(filepath.Dir(path))
		} else {
			skill.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
	}

	return skill, nil
}

// SplitFrontmatter extracts YAML frontmatter (between --- delimiters) from Markdown.
func SplitFrontmatter(content string) (frontmatter, body string) {
	content = strings.TrimSpace(content)

	if !strings.HasPrefix(content, "---") {
		return "", content
	}

	// Find the closing ---
	rest := content[3:]
	rest = strings.TrimLeft(rest, " \t")
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
		rest = rest[2:]
	}

	// Find a line whose trimmed content is exactly "---" (not "----" or
	// "---publish:"). Track byte offsets to slice frontmatter/body.
	endIdx := -1
	bodyStart := -1
	off := 0
	for {
		nl := strings.IndexByte(rest[off:], '\n')
		var line string
		if nl < 0 {
			line = rest[off:]
		} else {
			line = rest[off : off+nl]
		}
		if strings.TrimRight(line, "\r") == "---" {
			endIdx = off
			if nl < 0 {
				bodyStart = len(rest)
			} else {
				bodyStart = off + nl + 1
			}
			break
		}
		if nl < 0 {
			break
		}
		off += nl + 1
	}
	if endIdx < 0 {
		return "", content
	}

	frontmatter = strings.TrimRight(rest[:endIdx], "\n")
	body = rest[bodyStart:]

	return frontmatter, body
}

// FormatIndex returns a markdown index of every loaded skill (name +
// one-line description). Cheap to inject (~50 chars per skill) and gives
// the agent awareness of every skill it has access to, even when no skill
// matches the user's request closely. Full skill bodies are loaded on
// demand via the load_skill tool.
func (l *Loader) FormatIndex() string {
	l.mu.RLock()
	skills := l.skills
	l.mu.RUnlock()

	if len(skills) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\n## Skills Index\n\nThe following skills are loaded and available. Their full instructions are injected only when the user's request matches one of them; if a request relates to any of these but the full instructions are not present, ask the user to be more specific so the right skill can be loaded.\n\n")
	for _, s := range skills {
		b.WriteString("- **")
		b.WriteString(s.Name)
		b.WriteString("**")
		if s.Description != "" {
			b.WriteString(" — ")
			b.WriteString(s.Description)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// MissingBins returns the names of binaries declared in the skill's
// metadata.openclaw.requires.bins that are not currently on $PATH.
// Returns nil when the skill declares no required bins or all are present.
func MissingBins(s Skill) []string {
	bins := s.Metadata.OpenClaw.Requires.Bins
	if len(bins) == 0 {
		return nil
	}
	var missing []string
	for _, bin := range bins {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	return missing
}
