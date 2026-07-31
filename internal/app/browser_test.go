package app

import (
	"io"
	"reflect"
	"testing"
)

func TestLocalBrowserURL(t *testing.T) {
	tests := []struct {
		name string
		host string
		port string
		want string
	}{
		{name: "wildcard IPv4", host: "0.0.0.0", port: "8888", want: "http://127.0.0.1:8888/static/"},
		{name: "wildcard IPv6", host: "::", port: "9000", want: "http://[::1]:9000/static/"},
		{name: "specific host", host: "192.168.1.20", port: "8080", want: "http://192.168.1.20:8080/static/"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := localBrowserURL(test.host, test.port); got != test.want {
				t.Fatalf("localBrowserURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDefaultBrowserCommand(t *testing.T) {
	const targetURL = "http://127.0.0.1:8888/static/"
	tests := []struct {
		goos      string
		command   string
		args      []string
		wantError bool
	}{
		{goos: "darwin", command: "open", args: []string{targetURL}},
		{goos: "windows", command: "rundll32", args: []string{"url.dll,FileProtocolHandler", targetURL}},
		{goos: "linux", command: "xdg-open", args: []string{targetURL}},
		{goos: "plan9", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.goos, func(t *testing.T) {
			command, args, err := defaultBrowserCommand(test.goos, targetURL)
			if test.wantError {
				if err == nil {
					t.Fatal("defaultBrowserCommand() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("defaultBrowserCommand() error = %v", err)
			}
			if command != test.command || !reflect.DeepEqual(args, test.args) {
				t.Fatalf("defaultBrowserCommand() = %q, %v; want %q, %v", command, args, test.command, test.args)
			}
		})
	}
}

func TestOpenBrowserOnce(t *testing.T) {
	originalOutput := log.Out
	log.SetOutput(io.Discard)
	t.Cleanup(func() {
		log.SetOutput(originalOutput)
	})

	openCount := 0
	manager := &AppWebManager{
		openBrowser: true,
		browserOpener: func(string) error {
			openCount++
			return nil
		},
	}

	manager.openBrowserOnce("http://127.0.0.1:8888/static/")
	manager.openBrowserOnce("http://127.0.0.1:8888/static/")

	if openCount != 1 {
		t.Fatalf("browser opened %d times, want 1", openCount)
	}
}

func TestOpenBrowserOnceDisabled(t *testing.T) {
	openCount := 0
	manager := &AppWebManager{
		openBrowser: false,
		browserOpener: func(string) error {
			openCount++
			return nil
		},
	}

	manager.openBrowserOnce("http://127.0.0.1:8888/static/")

	if openCount != 0 {
		t.Fatalf("browser opened %d times, want 0", openCount)
	}
}
