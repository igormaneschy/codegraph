package index

// repositoryRoot carries a validated physical spelling through one index run.
// It is not permission to bypass securefile: every subsequent read still checks
// descriptors, and every repository observation still rehashes current inputs.
type repositoryRoot struct {
	path string
}

func validateRoot(path string) (repositoryRoot, error) {
	canonical, err := ValidateRepositoryRoot(path)
	if err != nil {
		return repositoryRoot{}, err
	}
	return repositoryRoot{path: canonical}, nil
}
