package agentkit

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

var fileStoreDirectoryLocks sync.Map

func fileStoreDirectoryLock(path string) (*sync.RWMutex, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	lock, _ := fileStoreDirectoryLocks.LoadOrStore(canonical, &sync.RWMutex{})
	return lock.(*sync.RWMutex), nil
}

func syncFileStoreDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
