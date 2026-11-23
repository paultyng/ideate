package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/paultyng/ideate/internal/atomicfile"
	"github.com/paultyng/ideate/internal/model"
)

const (
	summaryFilename = "summary.json"
	backupsDir      = ".backups"
)

// MigrationReport is the outcome of MigrateToOKF.
type MigrationReport struct {
	// BackupDir is the backup created for this run; empty when nothing
	// needed writing.
	BackupDir string
	// Migrated counts ideas written and read back.
	Migrated int
	// Folded counts migrated ideas whose description came from summary.json.
	Folded int
	// Failed maps slug to why that idea (or its summary.json) was skipped.
	Failed map[string]error
}

// migrationEntry is one idea the migration will write.
type migrationEntry struct {
	slug          string
	original      []byte
	originalMode  fs.FileMode
	out           []byte
	summary       []byte // nil when summary.json is absent
	summaryMode   fs.FileMode
	deleteSummary bool
	folded        bool
}

// legacySummary is the retired <slug>/summary.json sidecar shape.
type legacySummary struct {
	Line        string    `json:"line"`
	GeneratedAt time.Time `json:"generated_at"`
}

// MigrateToOKF rewrites every legacy v0.1 idea.md in OKF format and folds
// summary.json's line into an empty description. It backs up exactly the
// files it will change to <ideasDir>/.backups/okf-<ts>/ before writing any,
// verifies each new idea.md in memory before writing and on disk after.
// Ideas that fail a step are recorded in Failed and left untouched. A
// non-nil error means the backup could not be made and nothing was written.
func (s *FSStore) MigrateToOKF(ctx context.Context) (MigrationReport, error) {
	report := MigrationReport{Failed: map[string]error{}}
	plan, err := s.planOKFMigration(report.Failed)
	if err != nil {
		return report, err
	}
	if len(plan) == 0 {
		return report, nil
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	return s.applyOKFMigration(plan, report)
}

// planOKFMigration scans the ideas dir and prepares each idea in memory
// without touching disk.
func (s *FSStore) planOKFMigration(failed map[string]error) ([]migrationEntry, error) {
	entries, err := os.ReadDir(s.ideasDir)
	if err != nil {
		return nil, fmt.Errorf("reading ideas dir: %w", err)
	}
	var plan []migrationEntry
	for _, e := range entries {
		slug := e.Name()
		if !e.IsDir() || strings.HasPrefix(slug, ".") {
			continue
		}
		entry, ok, err := s.prepareOKFEntry(slug, failed)
		if err != nil {
			failed[slug] = errors.Join(failed[slug], err)
			continue
		}
		if ok {
			plan = append(plan, entry)
		}
	}
	return plan, nil
}

// prepareOKFEntry returns the plan entry for slug, ok=false when slug needs
// no write. A non-nil error means slug must not be written. A malformed
// summary.json is recorded in failed without blocking the idea itself.
func (s *FSStore) prepareOKFEntry(slug string, failed map[string]error) (migrationEntry, bool, error) {
	dir := s.ideaDir(slug)
	ideaPath := filepath.Join(dir, ideaFilename)
	info, err := os.Stat(ideaPath)
	if errors.Is(err, fs.ErrNotExist) {
		return migrationEntry{}, false, nil
	}
	if err != nil {
		return migrationEntry{}, false, fmt.Errorf("stat idea.md: %w", err)
	}
	data, err := os.ReadFile(ideaPath)
	if err != nil {
		return migrationEntry{}, false, fmt.Errorf("reading idea.md: %w", err)
	}
	legacy, err := model.IsLegacyIdeaFile(data)
	if err != nil {
		return migrationEntry{}, false, err
	}

	entry := migrationEntry{slug: slug, original: data, originalMode: info.Mode().Perm()}
	var summary *legacySummary
	summaryPath := filepath.Join(dir, summaryFilename)
	if sinfo, err := os.Stat(summaryPath); err == nil {
		sdata, err := os.ReadFile(summaryPath)
		if err != nil {
			failed[slug] = fmt.Errorf("reading summary.json: %w", err)
		} else {
			var sum legacySummary
			if err := json.Unmarshal(sdata, &sum); err != nil {
				failed[slug] = fmt.Errorf("parsing summary.json: %w", err)
			} else {
				summary = &sum
				entry.summary = sdata
				entry.summaryMode = sinfo.Mode().Perm()
				entry.deleteSummary = true
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		failed[slug] = fmt.Errorf("stat summary.json: %w", err)
	}
	if !legacy && summary == nil {
		return migrationEntry{}, false, nil
	}

	idea, err := model.ParseIdeaFile(string(data))
	if err != nil {
		return migrationEntry{}, false, err
	}
	if err := model.CheckLegacyParse(data, idea); err != nil {
		return migrationEntry{}, false, fmt.Errorf("raw-value guard: %w", err)
	}
	if summary != nil && summary.Line != "" && idea.Description == "" {
		idea.Description = summary.Line
		idea.Generated = summary.GeneratedAt
		entry.folded = true
	}

	out, err := model.SerializeIdeaFile(idea)
	if err != nil {
		return migrationEntry{}, false, err
	}
	if err := verifyMigratedIdea(idea, out); err != nil {
		return migrationEntry{}, false, fmt.Errorf("verifying converted idea: %w", err)
	}
	entry.out = []byte(out)

	if bytes.Equal(entry.out, data) && !entry.deleteSummary {
		return migrationEntry{}, false, nil
	}
	return entry, true, nil
}

// verifyMigratedIdea checks that out is non-legacy and parses back to want.
func verifyMigratedIdea(want *model.Idea, out string) error {
	legacy, err := model.IsLegacyIdeaFile([]byte(out))
	if err != nil {
		return err
	}
	if legacy {
		return errors.New("output still parses as legacy")
	}
	got, err := model.ParseIdeaFile(out)
	if err != nil {
		return err
	}
	// go-okf Concept.Bytes appends one trailing newline to a body lacking it.
	wantBody := want.Body
	if !strings.HasSuffix(wantBody, "\n") {
		wantBody += "\n"
	}
	switch {
	case got.Name != want.Name:
		return fmt.Errorf("name %q, want %q", got.Name, want.Name)
	case got.Status != want.Status:
		return fmt.Errorf("status %q, want %q", got.Status, want.Status)
	case got.Description != want.Description:
		return fmt.Errorf("description %q, want %q", got.Description, want.Description)
	case got.Body != wantBody:
		return errors.New("body changed")
	case (len(got.Resources) > 0 || len(want.Resources) > 0) && !reflect.DeepEqual(got.Resources, want.Resources):
		return fmt.Errorf("resources %+v, want %+v", got.Resources, want.Resources)
	case !got.Created.Equal(want.Created):
		return fmt.Errorf("created %v, want %v", got.Created, want.Created)
	case !got.Updated.Equal(want.Updated):
		return fmt.Errorf("updated %v, want %v", got.Updated, want.Updated)
	case !got.Generated.Equal(want.Generated):
		return fmt.Errorf("generated %v, want %v", got.Generated, want.Generated)
	case (got.PauseUntil == nil) != (want.PauseUntil == nil):
		return fmt.Errorf("pause_until %v, want %v", got.PauseUntil, want.PauseUntil)
	case got.PauseUntil != nil && !got.PauseUntil.Equal(*want.PauseUntil):
		return fmt.Errorf("pause_until %v, want %v", *got.PauseUntil, *want.PauseUntil)
	}
	return nil
}

// applyOKFMigration backs up then writes each plan entry.
func (s *FSStore) applyOKFMigration(plan []migrationEntry, report MigrationReport) (MigrationReport, error) {
	backup, err := s.backupOKFPlan(plan)
	if err != nil {
		return report, err
	}
	report.BackupDir = backup

	for _, entry := range plan {
		if err := s.writeOKFEntry(entry); err != nil {
			report.Failed[entry.slug] = errors.Join(report.Failed[entry.slug], err)
			continue
		}
		report.Migrated++
		if entry.folded {
			report.Folded++
		}
		if entry.deleteSummary {
			if err := os.Remove(filepath.Join(s.ideaDir(entry.slug), summaryFilename)); err != nil {
				report.Failed[entry.slug] = errors.Join(report.Failed[entry.slug], fmt.Errorf("removing summary.json: %w", err))
			}
		}
	}

	if err := s.regenerateIndexes(); err != nil {
		slog.Warn("regenerating okf indexes after migration", slog.Any("err", err))
	}
	return report, nil
}

// backupOKFPlan copies each entry's scanned idea.md and summary.json, plus
// the root index.md, into a new .backups/okf-<ts>/ dir. On any error it
// removes the new dir.
func (s *FSStore) backupOKFPlan(plan []migrationEntry) (string, error) {
	root := filepath.Join(s.ideasDir, backupsDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("creating backups dir: %w", err)
	}
	dir := filepath.Join(root, "okf-"+time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating backup dir: %w", err)
	}
	if err := copyOKFBackup(dir, s.ideasDir, plan); err != nil {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			err = errors.Join(err, fmt.Errorf("removing partial backup: %w", rmErr))
		}
		return "", fmt.Errorf("backing up: %w", err)
	}
	return dir, nil
}

func copyOKFBackup(dir, ideasDir string, plan []migrationEntry) error {
	for _, entry := range plan {
		slugDir := filepath.Join(dir, entry.slug)
		if err := os.Mkdir(slugDir, 0o755); err != nil {
			return err
		}
		if err := writeBackupFile(filepath.Join(slugDir, ideaFilename), entry.original, entry.originalMode); err != nil {
			return err
		}
		if entry.summary != nil {
			if err := writeBackupFile(filepath.Join(slugDir, summaryFilename), entry.summary, entry.summaryMode); err != nil {
				return err
			}
		}
	}
	indexPath := filepath.Join(ideasDir, indexFilename)
	info, err := os.Stat(indexPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return err
	}
	return writeBackupFile(filepath.Join(dir, indexFilename), data, info.Mode().Perm())
}

func writeBackupFile(path string, data []byte, mode fs.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// writeOKFEntry writes entry.out under the slug lock, refusing if idea.md
// changed since the scan, and confirms the bytes on disk afterwards.
func (s *FSStore) writeOKFEntry(entry migrationEntry) error {
	unlock := s.locks.Lock(entry.slug)
	defer unlock()

	ideaPath := filepath.Join(s.ideaDir(entry.slug), ideaFilename)
	current, err := os.ReadFile(ideaPath)
	if err != nil {
		return fmt.Errorf("re-reading idea.md: %w", err)
	}
	if !bytes.Equal(current, entry.original) {
		return errors.New("idea.md changed since scan")
	}
	if err := atomicfile.Write(ideaPath, entry.out, entry.originalMode); err != nil {
		return fmt.Errorf("writing idea.md: %w", err)
	}
	written, err := os.ReadFile(ideaPath)
	if err != nil {
		return fmt.Errorf("reading back idea.md: %w", err)
	}
	if !bytes.Equal(written, entry.out) {
		return errors.New("idea.md read-back mismatch")
	}
	return nil
}
