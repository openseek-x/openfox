//go:build !unix

package cliagent

import (
	"fmt"
	"os"
)

func openCodeBuddyCAFile(string) (*os.File, error) {
	return nil, fmt.Errorf("CodeBuddy requires macOS arm64")
}
