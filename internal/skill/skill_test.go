package skill

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantFM    string
		wantBody  string
	}{
		{
			name:     "with frontmatter",
			input:    "---\nname: test\n---\n# Body\nContent here",
			wantFM:   "name: test",
			wantBody: "# Body\nContent here",
		},
		{
			name:     "no frontmatter",
			input:    "# Just a heading\nSome content",
			wantFM:   "",
			wantBody: "# Just a heading\nSome content",
		},
		{
			name:     "empty",
			input:    "",
			wantFM:   "",
			wantBody: "",
		},
		{
			name:     "only frontmatter",
			input:    "---\nname: test\n---\n",
			wantFM:   "name: test",
			wantBody: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, body := splitFrontmatter(tt.input)
			assert.Equal(t, tt.wantFM, fm)
			assert.Equal(t, tt.wantBody, body)
		})
	}
}

func TestParseSkillFile(t *testing.T) {
	dir := t.TempDir()

	// Create a skill directory with SKILL.md
	skillDir := filepath.Join(dir, "web-search")
	os.MkdirAll(skillDir, 0o755)

	content := `---
name: web-search
description: Search the web for current information
tags:
  - search
  - web
  - internet
---

# Web Search Skill

When the user asks about current events, news, or information that may have
changed since your training cutoff, use the web_search tool.

## Usage Guidelines
- Keep queries concise (3-6 words)
- Verify claims from multiple sources
`

	skillPath := filepath.Join(skillDir, "SKILL.md")
	os.WriteFile(skillPath, []byte(content), 0o644)

	skill, err := parseSkillFile(skillPath)
	require.NoError(t, err)

	assert.Equal(t, "web-search", skill.Name)
	assert.Equal(t, "Search the web for current information", skill.Description)
	assert.Equal(t, []string{"search", "web", "internet"}, skill.Tags)
	assert.Contains(t, skill.Body, "Web Search Skill")
	assert.Contains(t, skill.Body, "Usage Guidelines")
}

func TestLoaderLoadFrom(t *testing.T) {
	dir := t.TempDir()

	// Create two skills
	for _, name := range []string{"skill-a", "skill-b"} {
		skillDir := filepath.Join(dir, name)
		os.MkdirAll(skillDir, 0o755)
		content := "---\nname: " + name + "\ndescription: Test skill " + name + "\n---\n\nBody of " + name
		os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644)
	}

	loader := NewLoader()
	err := loader.LoadFrom(dir)
	require.NoError(t, err)

	skills := loader.Skills()
	assert.Len(t, skills, 2)
}

func TestLoaderLoadFromDirectMD(t *testing.T) {
	dir := t.TempDir()

	// Create a skill as a direct .md file (not in a subdirectory)
	content := `---
name: gog
description: Google Workspace CLI for Gmail, Calendar, Drive
tags:
  - gmail
  - calendar
  - google
---

# gog

Use gog for Gmail/Calendar/Drive.
`
	os.WriteFile(filepath.Join(dir, "gog.md"), []byte(content), 0o644)

	// Also create a SKILL.md in a subdirectory (should still work)
	skillDir := filepath.Join(dir, "web-search")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: web-search\n---\nBody"), 0o644)

	loader := NewLoader()
	err := loader.LoadFrom(dir)
	require.NoError(t, err)

	skills := loader.Skills()
	assert.Len(t, skills, 2)

	// Find the direct .md skill
	var gogSkill *Skill
	for i := range skills {
		if skills[i].Name == "gog" {
			gogSkill = &skills[i]
			break
		}
	}
	require.NotNil(t, gogSkill)
	assert.Equal(t, "Google Workspace CLI for Gmail, Calendar, Drive", gogSkill.Description)
	assert.Contains(t, gogSkill.Tags, "gmail")
}

func TestLoaderLoadFromDirectMDDefaultName(t *testing.T) {
	dir := t.TempDir()

	// Skill file without a name in frontmatter — should use filename stem
	content := "---\ndescription: A test skill\n---\n\nBody here"
	os.WriteFile(filepath.Join(dir, "my-skill.md"), []byte(content), 0o644)

	loader := NewLoader()
	err := loader.LoadFrom(dir)
	require.NoError(t, err)

	skills := loader.Skills()
	require.Len(t, skills, 1)
	assert.Equal(t, "my-skill", skills[0].Name)
}

func TestLoaderSkipsMissingBinary(t *testing.T) {
	dir := t.TempDir()

	// Skill that requires a binary that doesn't exist
	content := `---
name: fake-tool
description: Requires a nonexistent binary
metadata:
  openclaw:
    requires:
      bins: ["this-binary-does-not-exist-xyz"]
---

Body here
`
	os.WriteFile(filepath.Join(dir, "fake-tool.md"), []byte(content), 0o644)

	// Skill with no binary requirement (should load fine)
	os.WriteFile(filepath.Join(dir, "simple.md"), []byte("---\nname: simple\n---\nBody"), 0o644)

	loader := NewLoader()
	err := loader.LoadFrom(dir)
	require.NoError(t, err)

	skills := loader.Skills()
	assert.Len(t, skills, 1)
	assert.Equal(t, "simple", skills[0].Name)
}

func TestLoaderLoadFromNonexistent(t *testing.T) {
	loader := NewLoader()
	err := loader.LoadFrom("/nonexistent/path")
	require.NoError(t, err) // should not error, just skip
	assert.Empty(t, loader.Skills())
}

func TestMatchSkills(t *testing.T) {
	loader := NewLoader()

	// Manually set skills for testing
	loader.skills = []Skill{
		{Name: "web-search", Description: "Search the web for current information", Tags: []string{"search", "web"}},
		{Name: "calendar", Description: "Manage calendar events and appointments", Tags: []string{"calendar", "schedule"}},
		{Name: "code-review", Description: "Review code for bugs and improvements", Tags: []string{"code", "review"}},
	}

	// Search-related query should match web-search
	matches := loader.MatchSkills("search the web for latest news", 3)
	assert.NotEmpty(t, matches)
	assert.Equal(t, "web-search", matches[0].Name)

	// Calendar-related query
	matches = loader.MatchSkills("what's on my calendar today?", 3)
	assert.NotEmpty(t, matches)
	assert.Equal(t, "calendar", matches[0].Name)

	// Unrelated query should return nothing
	matches = loader.MatchSkills("hello there", 3)
	assert.Empty(t, matches)
}

func TestFormatForPrompt(t *testing.T) {
	skills := []Skill{
		{Name: "test-skill", Body: "This is the body."},
	}

	result := FormatForPrompt(skills)
	assert.Contains(t, result, "## Available Skills")
	assert.Contains(t, result, "### test-skill")
	assert.Contains(t, result, "This is the body.")
}

func TestFormatForPromptEmpty(t *testing.T) {
	result := FormatForPrompt(nil)
	assert.Equal(t, "", result)
}

func TestFormatIndex(t *testing.T) {
	loader := NewLoader()
	loader.skills = []Skill{
		{Name: "pdftotext", Description: "Extract plain text from PDF files"},
		{Name: "ffmpeg", Description: "Process audio and video"},
		{Name: "noDesc"}, // skill without description still listed
	}

	got := loader.FormatIndex()
	assert.Contains(t, got, "## Skills Index")
	assert.Contains(t, got, "**pdftotext**")
	assert.Contains(t, got, "Extract plain text from PDF files")
	assert.Contains(t, got, "**ffmpeg**")
	assert.Contains(t, got, "Process audio and video")
	assert.Contains(t, got, "**noDesc**")
}

func TestFormatIndexEmpty(t *testing.T) {
	loader := NewLoader()
	assert.Equal(t, "", loader.FormatIndex())
}
