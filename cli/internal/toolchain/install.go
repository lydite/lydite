package toolchain

import (
	"os"
	"path/filepath"
)

// cacheRoot is the version-keyed directory layout every lydite-managed
// install already uses (internal/golang's gobin-<tool>-<version>,
// internal/rust's <tool>-<version>, internal/typescript's
// biome-toolchain-<versions>). Language toolchains join it rather than
// inventing a second location, so one cache key in CI covers all of them —
// which is exactly what wardnet's workflow already caches by path.
func cacheRoot(elem ...string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{base, "lydite"}, elem...)...), nil
}

// installOnce runs install into a fresh temp directory and moves the result
// into place atomically, so a download interrupted halfway can never leave a
// half-populated toolchain directory that the next run mistakes for a
// complete one. If dir already exists it is taken as a finished install and
// install is not called at all.
//
// The same-parent temp directory matters: os.Rename cannot cross filesystems,
// and os.TempDir is routinely a different mount from the user cache dir.
func installOnce(dir string, install func(staging string) error) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".staging-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if err := install(staging); err != nil {
		return err
	}
	if err := os.Rename(staging, dir); err != nil {
		// A concurrent lydite (two jobs sharing a cache volume) may have
		// won the race and created dir first. Its content is the same
		// verified archive, so that is a success, not a conflict.
		if _, statErr := os.Stat(dir); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}
