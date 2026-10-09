package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Lordymine/codegraph/internal/securefile"
)

type MatrixEdit struct {
	Root        string `json:"root"`
	Path        string `json:"path"`
	Backup      string `json:"backup"`
	Suffix      string `json:"suffix"`
	ExpectedSHA string `json:"expected_sha"`
	Restore     bool   `json:"restore"`
	CheckOnly   bool   `json:"check_only"`
}

type EditEvidence struct {
	OriginalSHA string `json:"original_sha"`
	EditedSHA   string `json:"edited_sha"`
}

// EditMatrixInput changes an admitted disposable input, never exporting its
// bytes. Restore verifies the original backup plus exact appended suffix, so a
// cancelled edit worker cannot lose restoration just by losing its JSON reply.
func EditMatrixInput(edit MatrixEdit) (EditEvidence, error) {
	if err := validateEdit(edit); err != nil {
		return EditEvidence{}, err
	}
	target := filepath.Join(edit.Root, filepath.FromSlash(edit.Path))
	original, err := securefile.ReadFile(target)
	if err != nil {
		return EditEvidence{}, err
	}
	if edit.Restore {
		return restoreMatrixInput(edit, target, original)
	}
	if digestBytes(original) != edit.ExpectedSHA {
		return EditEvidence{}, fmt.Errorf("edit %q input digest changed: expected %q", edit.Path, edit.ExpectedSHA)
	}
	if edit.CheckOnly {
		return EditEvidence{OriginalSHA: digestBytes(original)}, nil
	}
	if err := securefile.WritePrivate(edit.Backup, original); err != nil {
		return EditEvidence{}, err
	}
	replacement := append(original, []byte(edit.Suffix)...)
	if err := securefile.WritePrivate(target, replacement); err != nil {
		return EditEvidence{}, err
	}
	return EditEvidence{OriginalSHA: edit.ExpectedSHA, EditedSHA: digestBytes(replacement)}, nil
}

func restoreMatrixInput(edit MatrixEdit, target string, current []byte) (EditEvidence, error) {
	if digestBytes(current) == edit.ExpectedSHA {
		return EditEvidence{OriginalSHA: edit.ExpectedSHA}, nil
	}
	replacement, err := securefile.ReadFile(edit.Backup)
	if err != nil {
		return EditEvidence{}, err
	}
	if digestBytes(replacement) != edit.ExpectedSHA {
		return EditEvidence{}, fmt.Errorf("backup digest changed for %q", edit.Path)
	}
	changed := append(append([]byte(nil), replacement...), []byte(edit.Suffix)...)
	if digestBytes(current) != digestBytes(changed) {
		return EditEvidence{}, fmt.Errorf("concurrent modification of %q: backup retained; refusing overwrite", edit.Path)
	}
	if err := securefile.WritePrivate(target, replacement); err != nil {
		return EditEvidence{}, err
	}
	return EditEvidence{OriginalSHA: edit.ExpectedSHA, EditedSHA: digestBytes(current)}, nil
}

func validateEdit(edit MatrixEdit) error {
	if !filepath.IsAbs(edit.Root) || !filepath.IsAbs(edit.Backup) || edit.Path == "" || path.IsAbs(edit.Path) || path.Clean(edit.Path) != edit.Path || edit.Path == ".." || strings.HasPrefix(edit.Path, "../") || strings.ContainsAny(edit.Path, "\\:") || strings.IndexFunc(edit.Path, unicode.IsControl) >= 0 {
		return fmt.Errorf("unsafe edit root=%q path=%q backup=%q: want absolute root/backup and canonical relative input", edit.Root, edit.Path, edit.Backup)
	}
	for _, part := range strings.Split(edit.Path, "/") {
		if strings.HasPrefix(part, ".env") || part == "node_modules" || part == "vendor" || part == ".git" {
			return fmt.Errorf("edit path %q is private/dependency/metadata input", edit.Path)
		}
	}
	relative, err := filepath.Rel(edit.Root, edit.Backup)
	if err != nil || relative == ".." || !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("backup %q must be outside disposable root", edit.Backup)
	}
	if _, err := hex.DecodeString(edit.ExpectedSHA); err != nil || len(edit.ExpectedSHA) != 64 {
		return fmt.Errorf("invalid expected_sha %q: want SHA-256", edit.ExpectedSHA)
	}
	if !edit.CheckOnly && edit.Suffix == "" {
		return errors.New("edit/restore requires a nonempty exact suffix")
	}
	return nil
}

func digestBytes(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
