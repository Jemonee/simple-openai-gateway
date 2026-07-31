package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "projectctl:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return errors.New("missing command")
	}

	root, err := findRepositoryRoot()
	if err != nil {
		return err
	}

	switch args[0] {
	case "generate":
		if len(args) != 1 {
			return errors.New("generate does not accept arguments")
		}
		manifest, err := loadManifest(root)
		if err != nil {
			return err
		}
		if err := generateProjectMetadata(root, manifest); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "project metadata generated")
		return nil
	case "check":
		if len(args) != 1 {
			return errors.New("check does not accept arguments")
		}
		manifest, err := loadManifest(root)
		if err != nil {
			return err
		}
		if err := checkProject(root, manifest); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "project metadata is consistent")
		return nil
	case "field":
		if len(args) != 2 {
			return errors.New("usage: projectctl field <name>")
		}
		manifest, err := loadManifest(root)
		if err != nil {
			return err
		}
		value, err := manifestField(manifest, args[1])
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, value)
		return nil
	case "rename":
		return runRename(root, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runRename(root string, args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("rename", flag.ContinueOnError)
	flags.SetOutput(stderr)
	module := flags.String("module", "", "new Go module path")
	binary := flags.String("binary", "", "new binary name")
	app := flags.String("app", "", "new application name")
	display := flags.String("display", "", "new display name")
	description := flags.String("description", "", "new application description")
	tokenPrefix := flags.String("token-prefix", "", "new generated-token prefix")
	version := flags.String("version", "", "new version; defaults to the current value")
	favicon := flags.String("favicon", "", "new public favicon path; defaults to the current value")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected rename arguments: %s", strings.Join(flags.Args(), " "))
	}

	required := map[string]string{
		"--module":       *module,
		"--binary":       *binary,
		"--app":          *app,
		"--display":      *display,
		"--description":  *description,
		"--token-prefix": *tokenPrefix,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}

	current, err := loadManifest(root)
	if err != nil {
		return err
	}
	next := current
	next.GoModule = *module
	next.BinaryName = *binary
	next.AppName = *app
	next.DisplayName = *display
	next.Description = *description
	next.TokenPrefix = *tokenPrefix
	if *version != "" {
		next.Version = *version
	}
	if *favicon != "" {
		next.FaviconPath = *favicon
	}

	if err := renameProject(root, current, next); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "project renamed from %s to %s\n", current.GoModule, next.GoModule)
	return nil
}

func manifestField(manifest ProjectManifest, name string) (string, error) {
	switch name {
	case "goModule":
		return manifest.GoModule, nil
	case "binaryName":
		return manifest.BinaryName, nil
	case "appName":
		return manifest.AppName, nil
	case "displayName":
		return manifest.DisplayName, nil
	case "description":
		return manifest.Description, nil
	case "version":
		return manifest.Version, nil
	case "tokenPrefix":
		return manifest.TokenPrefix, nil
	case "faviconPath":
		return manifest.FaviconPath, nil
	default:
		return "", fmt.Errorf("unknown project field %q", name)
	}
}

func findRepositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if fileExists(filepath.Join(dir, manifestFile)) && fileExists(filepath.Join(dir, "go.mod")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not find project.json and go.mod")
		}
		dir = parent
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: projectctl <generate|check|field|rename>")
	fmt.Fprintln(w, "  generate                    regenerate Go and frontend metadata")
	fmt.Fprintln(w, "  check                       validate metadata and generated files")
	fmt.Fprintln(w, "  field <name>                print one project.json field")
	fmt.Fprintln(w, "  rename --module ...         change project identity and regenerate metadata")
}
