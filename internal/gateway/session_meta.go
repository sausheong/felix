package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/sausheong/felix/internal/config"
)

// sessionMetaMaxTitleLen caps title length to keep sidebar rows readable
// and to bound on-disk size of the sidecar.
const sessionMetaMaxTitleLen = 100

// sessionMetaPath returns the sidecar location for a session.
func sessionMetaPath(sessionsBase, agentID, sessionKey string) string {
	return filepath.Join(sessionsBase, agentID, sessionKey+".meta.json")
}

// validateSessionPathSegment rejects values that would let a JSON-RPC
// caller escape <sessionsBase>/<agentID>/ when joined into a sidecar
// path. Defence-in-depth: even though session.rename only fires after
// sessionStore.Exists succeeds, the underlying harness store doesn't
// validate either, and session.new (which can create the entry) is
// reachable from the same OAuth-gated WebSocket.
func validateSessionPathSegment(s string) error {
	if s == "" {
		return fmt.Errorf("path segment is empty")
	}
	if s == "." || s == ".." {
		return fmt.Errorf("path segment is reserved")
	}
	if strings.ContainsAny(s, `/\`+"\x00") {
		return fmt.Errorf("path segment contains separator or NUL")
	}
	return nil
}

type sessionMeta struct {
	Title string `json:"title"`
}

// readSessionMeta returns the title from the sidecar, or "" if the
// sidecar is missing or unreadable. Missing is the expected case for
// any session created before this feature shipped.
func readSessionMeta(sessionsBase, agentID, sessionKey string) string {
	data, err := os.ReadFile(sessionMetaPath(sessionsBase, agentID, sessionKey))
	if err != nil {
		return ""
	}
	var m sessionMeta
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	return m.Title
}

// writeSessionMeta atomically writes the title sidecar. Caller is
// responsible for validating the title via validateSessionTitle.
func writeSessionMeta(sessionsBase, agentID, sessionKey, title string) error {
	path := sessionMetaPath(sessionsBase, agentID, sessionKey)
	data, err := json.Marshal(sessionMeta{Title: title})
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(path, data, 0o600)
}

// validateSessionTitle enforces length cap and rejects ASCII control
// chars (defensive — title is rendered via escHtml client-side but we
// don't want NULs or newlines on disk).
func validateSessionTitle(title string) error {
	if !utf8.ValidString(title) {
		return fmt.Errorf("title is not valid UTF-8")
	}
	if utf8.RuneCountInString(title) > sessionMetaMaxTitleLen {
		return fmt.Errorf("title exceeds %d characters", sessionMetaMaxTitleLen)
	}
	for _, r := range title {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("title contains control character")
		}
	}
	if strings.ContainsAny(title, "/\\") {
		return fmt.Errorf("title cannot contain path separators")
	}
	return nil
}
