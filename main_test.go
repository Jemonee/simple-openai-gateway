package main

import "testing"

func TestParseStartupOptions(t *testing.T) {
	tests := []struct {
		name              string
		args              []string
		wantDisableIFrame bool
	}{
		{name: "default allows iframe embedding"},
		{name: "disable iframe", args: []string{"--disable-iframe"}, wantDisableIFrame: true},
		{name: "explicitly allow iframe", args: []string{"--disable-iframe=false"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, err := parseStartupOptions(test.args)
			if err != nil {
				t.Fatalf("parseStartupOptions() error = %v", err)
			}
			if options.DisableIFrame != test.wantDisableIFrame {
				t.Fatalf("DisableIFrame = %t, want %t", options.DisableIFrame, test.wantDisableIFrame)
			}
		})
	}
}

func TestParseStartupOptionsRejectsUnexpectedArgument(t *testing.T) {
	if _, err := parseStartupOptions([]string{"unexpected"}); err == nil {
		t.Fatal("parseStartupOptions() error = nil, want an error")
	}
}
