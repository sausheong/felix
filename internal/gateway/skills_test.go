package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateSkillName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple", "cortex.md", false},
		{"with dashes and underscores", "my-skill_v2.md", false},
		{"with digits", "skill123.md", false},
		{"with dots", "skill.v2.md", false},
		{"empty", "", true},
		{"no .md extension", "cortex", true},
		{"wrong extension", "cortex.txt", true},
		{"path separator forward", "foo/bar.md", true},
		{"path separator back", "foo\\bar.md", true},
		{"parent traversal", "../foo.md", true},
		{"space", "foo bar.md", true},
		{"colon", "foo:bar.md", true},
		{"unicode", "fööö.md", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSkillName(tt.input)
			if tt.wantErr {
				assert.Error(t, err, "input %q", tt.input)
			} else {
				assert.NoError(t, err, "input %q", tt.input)
			}
		})
	}
}
