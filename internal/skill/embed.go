package skill

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

//go:embed bundled/*.md
var bundledSkillsFS embed.FS

// SeedBundledSkills writes the embedded starter skills into dir, but only
// if dir is empty. Existing files are never touched. Returns the names
// (basenames) of the files written, or nil if seeding was skipped.
//
// Called once on Felix startup so a freshly-installed user gets a working
// set of skills (cortex, ffmpeg, imagemagick, pandoc, pdftotext) without
// having to copy from the repo.
func SeedBundledSkills(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read skills dir: %w", err)
	}
	// Any pre-existing entry (file or subdir) means the user has touched
	// this directory; do not seed. This protects users who have
	// intentionally removed bundled skills.
	if len(entries) > 0 {
		return nil, nil
	}

	bundled, err := fs.ReadDir(bundledSkillsFS, "bundled")
	if err != nil {
		return nil, fmt.Errorf("read bundled skills: %w", err)
	}
	var written []string
	for _, b := range bundled {
		if b.IsDir() {
			continue
		}
		data, err := fs.ReadFile(bundledSkillsFS, filepath.Join("bundled", b.Name()))
		if err != nil {
			return written, fmt.Errorf("read bundled %s: %w", b.Name(), err)
		}
		dst := filepath.Join(dir, b.Name())
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return written, fmt.Errorf("write %s: %w", dst, err)
		}
		written = append(written, b.Name())
	}
	if len(written) > 0 {
		slog.Info("seeded bundled skills", "dir", dir, "count", len(written), "names", written)
	}
	return written, nil
}
