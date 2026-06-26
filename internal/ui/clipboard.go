package ui

import (
	"fmt"
	"os/exec"
	"strings"
)

// writeClipboard writes text to the system clipboard using the first available tool.
func writeClipboard(text string) error {
	candidates := [][]string{
		{"wl-copy"},
		{"xclip", "-selection", "clipboard"},
		{"xsel", "--clipboard", "--input"},
		{"pbcopy"},
	}
	for _, args := range candidates {
		if _, err := exec.LookPath(args[0]); err == nil {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Stdin = strings.NewReader(text)
			return cmd.Run()
		}
	}
	return fmt.Errorf("no clipboard tool found (install wl-copy or xclip)")
}
