package skills

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const skillFileName = "SKILL.md"

// Leaf is the last path segment of a library name (workflows/one-by-one → one-by-one).
func Leaf(name string) string {
	name = strings.TrimSuffix(name, "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// DestDir joins a resolved skills root with the skill leaf. It rejects a
// name whose leaf is empty, "." , "..", or contains a path separator, so
// install and uninstall cannot escape the skills root.
func DestDir(root, name string) (string, error) {
	leaf, err := destLeaf(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, leaf), nil
}

func destLeaf(name string) (string, error) {
	leaf := Leaf(name)
	if leaf == "" || leaf == "." || leaf == ".." {
		return "", fmt.Errorf("invalid skill dest name %q", name)
	}
	if strings.ContainsAny(leaf, `/\`) {
		return "", fmt.Errorf("invalid skill dest name %q", name)
	}
	if filepath.Clean(leaf) != leaf {
		return "", fmt.Errorf("invalid skill dest name %q", name)
	}
	return leaf, nil
}

// SkillFile returns the SKILL.md path under a dest or fetched tree.
func SkillFile(dir string) string {
	return filepath.Join(dir, skillFileName)
}

// MaterialisableFiles lists relative paths install would copy from srcDir,
// omitting cue.mod/ and skill.cue. Paths use forward slashes.
func MaterialisableFiles(srcDir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if omitRel(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// Materialise replaces destDir with the fetched tree minus cue.mod/ and skill.cue.
// srcDir is never mutated; the copy is staged first so the module cache is untouched.
func Materialise(srcDir, destDir string) error {
	tmp, err := os.MkdirTemp("", "start-skill-*")
	if err != nil {
		return fmt.Errorf("creating staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	staging := filepath.Join(tmp, "bundle")
	if err := copyFiltered(srcDir, staging); err != nil {
		return err
	}
	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("removing %s: %w", destDir, err)
	}
	if err := os.MkdirAll(filepath.Dir(destDir), 0o755); err != nil {
		return fmt.Errorf("creating parent of %s: %w", destDir, err)
	}
	if err := copyAll(staging, destDir); err != nil {
		return fmt.Errorf("writing %s: %w", destDir, err)
	}
	return nil
}

func omitRel(rel string) bool {
	first, rest, _ := strings.Cut(filepath.ToSlash(rel), "/")
	if first == "cue.mod" {
		return true
	}
	if first == "skill.cue" && rest == "" {
		return true
	}
	return false
}

func copyFiltered(src, dest string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dest, 0o755)
		}
		if omitRel(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyAll(src, dest string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := dest
		if rel != "." {
			target = filepath.Join(dest, rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm()|0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
