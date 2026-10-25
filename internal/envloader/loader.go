// Package envloader loads optional dotenv configuration without replacing
// environment variables inherited from the host process.
package envloader

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

const maxDotenvBytes = 1 << 20

// Load reads .env, .env.local, and (when nonempty) explicitPath, in increasing
// order of precedence. Relative paths are resolved against cwd. Values already
// present in the process environment, including empty values, always win.
//
// Missing default files are optional; an explicit path must refer to an
// existing regular file. Existing files that cannot be read or parsed fail the
// load without applying any of the values collected from earlier files.
func Load(cwd, explicitPath string) error {
	root, err := filepath.Abs(cwd)
	if err != nil {
		return fmt.Errorf("resolve dotenv directory: %w", err)
	}

	merged := make(map[string]string)
	for _, name := range []string{".env", ".env.local"} {
		values, err := readFile(filepath.Join(root, name), true)
		if err != nil {
			return err
		}
		for key, value := range values {
			merged[key] = value
		}
	}

	if explicitPath != "" {
		path := explicitPath
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		values, err := readFile(path, false)
		if err != nil {
			return err
		}
		for key, value := range values {
			merged[key] = value
		}
	}

	for key := range merged {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return errors.New("dotenv contains an invalid environment variable name")
		}
	}
	for key, value := range merged {
		if _, present := os.LookupEnv(key); present {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set dotenv variable %q: %w", key, err)
		}
	}
	return nil
}

func readFile(path string, optional bool) (map[string]string, error) {
	f, err := openDotenv(path)
	if err != nil {
		if optional && errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		// OS error strings may contain sensitive paths supplied by callers.
		return nil, fmt.Errorf("dotenv file %q cannot be opened securely: %s", filepath.Base(path), dotenvOpenCause(err))
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect dotenv file %q: file metadata unavailable", filepath.Base(path))
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("dotenv file %q must be a regular file (not a symlink)", filepath.Base(path))
	}

	if info.Size() > maxDotenvBytes {
		return nil, fmt.Errorf("dotenv file %q exceeds %d bytes", filepath.Base(path), maxDotenvBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxDotenvBytes+1))
	if err != nil || len(data) > maxDotenvBytes {
		return nil, fmt.Errorf("dotenv file %q cannot be read safely (I/O error or size limit)", filepath.Base(path))
	}
	values, err := godotenv.Parse(bytes.NewReader(data))
	if err != nil {
		// Parser errors may quote the offending line, which could contain secrets.
		return nil, fmt.Errorf("dotenv file %q is unreadable or has invalid syntax", filepath.Base(path))
	}
	return values, nil
}

func dotenvOpenCause(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "file does not exist"
	case errors.Is(err, os.ErrPermission):
		return "access denied"
	default:
		return "not a safe regular file or I/O failure"
	}
}
