package sensors

import (
	"errors"
	"io/fs"
	"path/filepath"
)

// DirBytes is the apparent size of every regular file under path; symlinks are not followed.
func DirBytes(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, addSize(&total))
	return total, err
}

// addSize is DirBytes' walk: each regular file's size added to total. A file or directory gone
// mid-walk (a recording renamed or pruned) is skipped; a missing root (d nil) still fails.
func addSize(total *int64) fs.WalkDirFunc {
	return func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		*total += info.Size()
		return nil
	}
}
