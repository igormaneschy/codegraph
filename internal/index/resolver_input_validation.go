package index

import (
	"errors"
	"fmt"
)

func validateResolverInputPlan(plan ResolverInputPlan) error {
	if plan.Version != resolverInputVersion {
		return fmt.Errorf("unsupported resolver input plan version %q", plan.Version)
	}
	if dependencies := plan.GoDependencies; dependencies != nil {
		if dependencies.Version != goDependencyInputVersion || !validSHA256(dependencies.Digest) ||
			dependencies.Files < 0 || dependencies.Bytes < 0 {
			return errors.New("manifest has an invalid Go dependency input identity")
		}
	}
	last := ""
	for _, file := range plan.Files {
		if err := validatePlannedPath(file.Path); err != nil {
			return err
		}
		if !validSHA256(file.SHA256) || file.Path <= last || isPrivateEnvironmentFile(file.Path) {
			return fmt.Errorf("invalid or unordered resolver input %q", file.Path)
		}
		last = file.Path
	}
	if err := validatePlannedDirectories(plan.Directories); err != nil {
		return err
	}
	return validatePlannedLinks(plan.Links)
}

func validatePlannedPath(path string) error {
	clean, err := cleanResolverRelativePath(path)
	if err != nil || clean != path {
		return fmt.Errorf("resolver input %q must be a canonical repository-relative path", path)
	}
	return nil
}

func validatePlannedDirectories(directories []string) error {
	last := ""
	for _, dir := range directories {
		if err := validatePlannedPath(dir); err != nil {
			return err
		}
		if dir <= last {
			return fmt.Errorf("resolver directories are not ordered at %q", dir)
		}
		last = dir
	}
	return nil
}

func validatePlannedLinks(links []ResolverInputLink) error {
	last := ""
	for _, link := range links {
		if err := validatePlannedPath(link.Path); err != nil {
			return err
		}
		if err := validatePlannedPath(link.Target); err != nil {
			return err
		}
		if link.Path <= last {
			return fmt.Errorf("resolver links are not ordered at %q", link.Path)
		}
		last = link.Path
	}
	return nil
}
