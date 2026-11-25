// Package browser opens URLs in the user's default browser.
package browser

import (
	"os/exec"
	"runtime"
)

// Open starts the platform's URL opener for url without waiting for it.
func Open(url string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Start()
	case "linux":
		exec.Command("xdg-open", url).Start()
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
}
