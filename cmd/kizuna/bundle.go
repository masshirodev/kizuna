package main

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"
)

const bundleVersion = 1

type profileBundleManifest struct {
	Version   int     `json:"version"`
	Profile   Profile `json:"profile"`
	CreatedAt string  `json:"created_at"`
}

func exportProfile(profile Profile, bundlePath string, stdout io.Writer) error {
	if _, err := os.Stat(bundlePath); err == nil {
		return fmt.Errorf("bundle %q already exists; refusing to overwrite", bundlePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	workdir, err := ensureProfileState(profile)
	if err != nil {
		return err
	}
	unlock, err := acquireProfileLock(workdir)
	if err != nil {
		return err
	}
	defer unlock()

	if err := os.MkdirAll(filepath.Dir(bundlePath), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(bundlePath), ".profile-export-*.age")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Remove(tmpPath); err != nil {
		return err
	}

	cmd := exec.Command("age", "-p", "-o", tmpPath)
	cmd.Stderr = os.Stderr
	archiveIn, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return ageCommandError(err)
	}
	archiveErr := writeProfileBundle(archiveIn, profile, workdir)
	closeErr := archiveIn.Close()
	waitErr := cmd.Wait()
	if archiveErr != nil {
		return archiveErr
	}
	if closeErr != nil {
		return closeErr
	}
	if waitErr != nil {
		return fmt.Errorf("age encryption failed: %w", waitErr)
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, bundlePath); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "exported %s to %s\n", profile.Name, bundlePath)
	return nil
}

func importProfile(bundlePath string, cfg *Config, configFile string, stdout io.Writer) error {
	if _, err := os.Stat(bundlePath); err != nil {
		return err
	}
	root, err := profileRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(root, ".profile-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	cmd := exec.Command("age", "-d", bundlePath)
	cmd.Stderr = os.Stderr
	archiveOut, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return ageCommandError(err)
	}
	manifest, err := extractProfileBundle(archiveOut, stage)
	if err != nil {
		_ = archiveOut.Close()
	}
	waitErr := cmd.Wait()
	if err != nil {
		return err
	}
	if waitErr != nil {
		return fmt.Errorf("age decryption failed: %w", waitErr)
	}
	if err := validateBundleManifest(manifest); err != nil {
		return err
	}
	stateInfo, err := os.Stat(filepath.Join(stage, "state"))
	if err != nil {
		return errors.New("bundle has no profile state")
	}
	if !stateInfo.IsDir() {
		return errors.New("bundle profile state is not a directory")
	}
	if _, err := findProfile(*cfg, manifest.Profile.Name); err == nil {
		return fmt.Errorf("profile %q already exists; refusing to replace it", manifest.Profile.Name)
	}

	finalDir := filepath.Join(root, manifest.Profile.Name)
	if _, err := os.Stat(finalDir); err == nil {
		return fmt.Errorf("profile state %q already exists; refusing to replace it", manifest.Profile.Name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(filepath.Join(stage, "state"), finalDir); err != nil {
		return err
	}
	cfg.Profiles = append(cfg.Profiles, manifest.Profile)
	if err := saveConfig(configFile, *cfg); err != nil {
		_ = os.Rename(finalDir, filepath.Join(stage, "state"))
		return err
	}
	fmt.Fprintf(stdout, "imported %s (%s)\n", manifest.Profile.Name, manifest.Profile.Provider)
	return nil
}

func writeProfileBundle(dst io.Writer, profile Profile, workdir string) error {
	tarWriter := tar.NewWriter(dst)
	manifest := profileBundleManifest{Version: bundleVersion, Profile: profile, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		_ = tarWriter.Close()
		return err
	}
	if err := writeTarFile(tarWriter, "manifest.json", manifestData, 0600); err != nil {
		_ = tarWriter.Close()
		return err
	}
	walkErr := filepath.Walk(workdir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(workdir, path)
		if err != nil {
			return err
		}
		if relative == "." || relative == ".active.lock" {
			return nil
		}
		// Concurrent Codex and Claude launches keep only process locks here.
		// It is runtime bookkeeping, not portable profile state.
		if relative == instancesDirectory && info.IsDir() {
			return filepath.SkipDir
		}
		// Codex creates executable helper symlinks beneath this directory for
		// each running invocation. They are host-specific, short-lived runtime
		// state and are recreated automatically on the destination machine.
		if profile.Provider == "codex" && relative == filepath.Join("codex", "tmp") && info.IsDir() {
			return filepath.SkipDir
		}
		// OpenCode plugins can install npm dependencies under its config tree.
		// node_modules is generated from the package manifest and lockfile, is
		// host-specific, and includes executable symlinks in .bin. Keep the
		// source configuration but omit those reinstallable dependencies.
		if profile.Provider == "opencode" && info.IsDir() && filepath.Base(relative) == "node_modules" {
			return filepath.SkipDir
		}
		// Antigravity has to run with a profile-local HOME. Its general cache and
		// generated CLI runtime trees are host-specific and reproducible; keep
		// the settings, OAuth token, conversations, and other persistent state.
		if profile.Provider == "antigravity" {
			if relative == filepath.Join("home", ".cache") && info.IsDir() {
				return filepath.SkipDir
			}
			agyData := filepath.Join("home", ".gemini", "antigravity-cli")
			for _, runtimeName := range []string{"bin", "builtin", "crashes", "log", "updater"} {
				if relative == filepath.Join(agyData, runtimeName) && info.IsDir() {
					return filepath.SkipDir
				}
			}
			if relative == filepath.Join(agyData, "cli.log") && info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to export symlink %q", relative)
		}
		name := filepath.ToSlash(filepath.Join("state", relative))
		if info.IsDir() {
			return writeTarDirectory(tarWriter, name, int64(info.Mode().Perm()))
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to export special file %q", relative)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err == nil {
			header.Name = name
			err = tarWriter.WriteHeader(header)
		}
		if err == nil {
			_, err = io.Copy(tarWriter, file)
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
	closeErr := tarWriter.Close()
	if walkErr != nil {
		return walkErr
	}
	return closeErr
}

func extractProfileBundle(src io.Reader, stage string) (profileBundleManifest, error) {
	reader := tar.NewReader(src)
	var manifest profileBundleManifest
	manifestSeen := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return manifest, err
		}
		tarPath := filepath.ToSlash(header.Name)
		cleanTarPath := pathpkg.Clean(tarPath)
		if strings.HasPrefix(tarPath, "/") || cleanTarPath == ".." || strings.HasPrefix(cleanTarPath, "../") {
			return manifest, fmt.Errorf("unsafe bundle path %q", header.Name)
		}
		switch header.Name {
		case "manifest.json":
			if manifestSeen || header.Typeflag != tar.TypeReg {
				return manifest, errors.New("bundle has invalid manifest")
			}
			if err := json.NewDecoder(io.LimitReader(reader, 1<<20)).Decode(&manifest); err != nil {
				return manifest, fmt.Errorf("read bundle manifest: %w", err)
			}
			manifestSeen = true
		default:
			if !strings.HasPrefix(filepath.ToSlash(header.Name), "state/") {
				return manifest, fmt.Errorf("bundle contains unexpected path %q", header.Name)
			}
			if err := extractTarEntry(reader, stage, header); err != nil {
				return manifest, err
			}
		}
	}
	if !manifestSeen {
		return manifest, errors.New("bundle has no manifest")
	}
	return manifest, nil
}

func extractTarEntry(reader *tar.Reader, stage string, header *tar.Header) error {
	relative := filepath.FromSlash(header.Name)
	target := filepath.Join(stage, relative)
	if header.Typeflag == tar.TypeDir {
		return os.MkdirAll(target, 0700)
	}
	if header.Typeflag != tar.TypeReg {
		return fmt.Errorf("bundle contains unsupported entry %q", header.Name)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyN(file, reader, header.Size)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func writeTarFile(writer *tar.Writer, name string, data []byte, mode int64) error {
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data))}); err != nil {
		return err
	}
	_, err := writer.Write(data)
	return err
}

func writeTarDirectory(writer *tar.Writer, name string, mode int64) error {
	if !strings.HasSuffix(name, "/") {
		name += "/"
	}
	return writer.WriteHeader(&tar.Header{Name: name, Mode: mode, Typeflag: tar.TypeDir})
}

func validateBundleManifest(manifest profileBundleManifest) error {
	if manifest.Version != bundleVersion {
		return fmt.Errorf("unsupported profile bundle version %d", manifest.Version)
	}
	if !validName(manifest.Profile.Name) {
		return fmt.Errorf("invalid profile name %q in bundle", manifest.Profile.Name)
	}
	if manifest.Profile.Provider == "" || manifest.Profile.Command == "" {
		return errors.New("bundle profile is missing provider or command")
	}
	return nil
}

func ageCommandError(err error) error {
	var pathErr *exec.Error
	if errors.As(err, &pathErr) {
		return errors.New("age is required for profile bundles; install age and try again")
	}
	return err
}
