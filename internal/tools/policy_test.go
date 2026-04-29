package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPolicyAllowAll(t *testing.T) {
	p := Policy{} // empty allow/deny = allow all
	assert.True(t, p.IsAllowed("bash"))
	assert.True(t, p.IsAllowed("read_file"))
	assert.True(t, p.IsAllowed("anything"))
}

func TestPolicyAllowList(t *testing.T) {
	p := Policy{
		Allow: []string{"read_file", "write_file"},
	}
	assert.True(t, p.IsAllowed("read_file"))
	assert.True(t, p.IsAllowed("write_file"))
	assert.False(t, p.IsAllowed("bash"))
	assert.False(t, p.IsAllowed("edit_file"))
}

func TestPolicyDenyList(t *testing.T) {
	p := Policy{
		Deny: []string{"bash"},
	}
	assert.False(t, p.IsAllowed("bash"))
	assert.True(t, p.IsAllowed("read_file"))
	assert.True(t, p.IsAllowed("write_file"))
}

func TestPolicyAllowAndDeny(t *testing.T) {
	// Allow overridden by deny
	p := Policy{
		Allow: []string{"read_file", "bash"},
		Deny:  []string{"bash"},
	}
	assert.True(t, p.IsAllowed("read_file"))
	assert.False(t, p.IsAllowed("bash"))   // denied despite being in allow
	assert.False(t, p.IsAllowed("web_fetch")) // not in allow
}

func TestPolicyWildcardAllow(t *testing.T) {
	p := Policy{
		Allow: []string{"*"},
		Deny:  []string{"bash"},
	}
	assert.True(t, p.IsAllowed("read_file"))
	assert.False(t, p.IsAllowed("bash"))
}

func TestPolicyWildcardDeny(t *testing.T) {
	p := Policy{
		Deny: []string{"*"},
	}
	assert.False(t, p.IsAllowed("bash"))
	assert.False(t, p.IsAllowed("read_file"))
}

