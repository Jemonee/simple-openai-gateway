package app

import (
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

func localBrowserURL(host string, port string) string {
	browserHost := strings.Trim(strings.TrimSpace(host), "[]")
	switch browserHost {
	case "", "0.0.0.0":
		browserHost = "127.0.0.1"
	case "::":
		browserHost = "::1"
	}
	return (&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(browserHost, port),
		Path:   "/static/",
	}).String()
}

func defaultBrowserCommand(goos string, targetURL string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "open", []string{targetURL}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", targetURL}, nil
	case "linux":
		return "xdg-open", []string{targetURL}, nil
	default:
		return "", nil, fmt.Errorf("unsupported operating system %q", goos)
	}
}

func openDefaultBrowser(targetURL string) error {
	command, args, err := defaultBrowserCommand(runtime.GOOS, targetURL)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath(command); err != nil {
		return fmt.Errorf("default browser launcher %q is unavailable: %w", command, err)
	}
	if err := exec.Command(command, args...).Run(); err != nil {
		return fmt.Errorf("open %s: %w", targetURL, err)
	}
	return nil
}

func (awm *AppWebManager) openBrowserOnce(targetURL string) {
	if !awm.openBrowser || awm.browserOpener == nil {
		return
	}
	awm.browserOpenOnce.Do(func() {
		if err := awm.browserOpener(targetURL); err != nil {
			log.Warnf("unable to open the default browser: %v", err)
			return
		}
		log.Infof("opened the default browser at %s", targetURL)
	})
}
