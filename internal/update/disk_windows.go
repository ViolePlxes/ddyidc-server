package update

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func diskFree(path string) (uint64, error) {
	volume := filepath.VolumeName(path) + `\`
	pointer, err := windows.UTF16PtrFromString(volume)
	if err != nil {
		return 0, err
	}
	var available uint64
	err = windows.GetDiskFreeSpaceEx(pointer, &available, nil, nil)
	return available, err
}
