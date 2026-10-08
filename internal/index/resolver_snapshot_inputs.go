package index

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/Lordymine/codegraph/internal/securefile"
)

func stageResolverInputPlan(ctx context.Context, root, snapshot string, plan ResolverInputPlan, copied map[string]bool, verify func() error) error {
	for _, dir := range plan.Directories {
		if err := checkResolverStaging(ctx, verify); err != nil {
			return err
		}
		if err := securefile.MkdirAllPrivate(filepath.Join(snapshot, filepath.FromSlash(dir))); err != nil {
			return err
		}
	}
	for _, file := range plan.Files {
		if err := checkResolverStaging(ctx, verify); err != nil {
			return err
		}
		if copied[file.Path] {
			if hash, err := hashFile(filepath.Join(snapshot, file.Path)); err != nil || hash != file.SHA256 {
				return fmt.Errorf("resolver input %q has inconsistent observed bytes", file.Path)
			}
			continue
		}
		if err := copyResolverFile(ctx, root, snapshot, file.Path, file.SHA256); err != nil {
			return err
		}
		copied[file.Path] = true
	}
	for _, link := range plan.Links {
		if err := checkResolverStaging(ctx, verify); err != nil {
			return err
		}
		if err := stageResolverLink(root, snapshot, link); err != nil {
			return err
		}
	}
	return nil
}

func stageResolverLink(root, snapshot string, link ResolverInputLink) error {
	target, err := observeDependencyLink(root, link.Path)
	if err != nil {
		return err
	}
	if target != link.Target {
		return fmt.Errorf("%w: dependency link %q changed before staging", securefile.ErrUnsafePath, link.Path)
	}
	dst := filepath.Join(snapshot, filepath.FromSlash(link.Path))
	if err := securefile.MkdirAllPrivate(filepath.Dir(dst)); err != nil {
		return err
	}
	relative, err := filepath.Rel(filepath.Dir(dst), filepath.Join(snapshot, filepath.FromSlash(target)))
	if err != nil {
		return err
	}
	return securefile.SymlinkPrivate(relative, dst)
}

func checkResolverStaging(ctx context.Context, verify func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return verify()
}
