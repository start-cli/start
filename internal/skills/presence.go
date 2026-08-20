package skills

import (
	"fmt"
	"os"
)

// PresentDests returns dest directories under roots that contain SKILL.md for
// key. Roots are not enumerated for unknown leaves; presence is per key.
// NotExist is absence. Any other Stat error is returned, not treated as missing.
func PresentDests(roots []Dest, key string) ([]string, error) {
	var out []string
	for _, r := range roots {
		dest, err := DestDir(r.Root, key)
		if err != nil {
			return nil, err
		}
		path := SkillFile(dest)
		_, err = os.Stat(path)
		if err == nil {
			out = append(out, dest)
			continue
		}
		if os.IsNotExist(err) {
			continue
		}
		return nil, fmt.Errorf("checking %s: %w", path, err)
	}
	return out, nil
}
