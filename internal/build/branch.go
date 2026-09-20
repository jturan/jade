package build

import "github.com/jturan/jade/internal/gitx"

// branchName is indirected through gitx so the loop's tests do not need a repo.
func branchName(number int, title string) string {
	return gitx.BranchName(number, title)
}
