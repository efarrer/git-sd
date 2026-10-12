package main

import (
	"fmt"
	"git-sd/internal/git"
)

func runTrack(args []string) {
	_, _, since, err := git.ResolveBaseBranch()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		exitCleanly(1)
	}

	if err := git.RewriteCommitsWithIDs(since); err != nil {
		fmt.Printf("Error tagging commits: %v\n", err)
		exitCleanly(1)
	}

	fmt.Println("Commits tracked.")
}
