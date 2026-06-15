package singleserver

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type StorageBackupResult struct {
	Path        string
	Files       int
	SQLiteFiles int
}

func backupRoot() string {
	return envDefault("SINGLESERVER_BACKUP_DIR", "/srv/backups")
}

func resolveBackupPath(appName string, value string) string {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "/") || strings.HasSuffix(value, ".tar.gz") {
		return value
	}
	return filepath.Join(backupRoot(), appName, value+".tar.gz")
}

func createStorageBackup(appName string, storagePath string, backupDir string, w io.Writer) (StorageBackupResult, error) {
	progress := newBackupProgress(w)
	defer progress.finish()

	if _, err := os.Stat(storagePath); err != nil {
		return StorageBackupResult{}, err
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return StorageBackupResult{}, err
	}
	backupPath, err := nextBackupPath(backupDir, time.Now())
	if err != nil {
		return StorageBackupResult{}, err
	}
	snapshotDir, err := os.MkdirTemp(backupDir, ".snapshot-"+appName+"-*")
	if err != nil {
		return StorageBackupResult{}, err
	}
	defer os.RemoveAll(snapshotDir)

	files, sqliteFiles, err := snapshotStorage(storagePath, snapshotDir, progress)
	if err != nil {
		return StorageBackupResult{}, err
	}
	if err := writeTarGz(snapshotDir, backupPath, progress); err != nil {
		return StorageBackupResult{}, err
	}
	return StorageBackupResult{Path: backupPath, Files: files, SQLiteFiles: sqliteFiles}, nil
}

func nextBackupPath(backupDir string, now time.Time) (string, error) {
	backupID := now.UTC().Format("20060102T150405Z")
	for suffix := 0; suffix < 1000; suffix++ {
		name := backupID
		if suffix > 0 {
			name = fmt.Sprintf("%s-%d", backupID, suffix)
		}
		path := filepath.Join(backupDir, name+".tar.gz")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("could not allocate backup path for %s", backupID)
}

// scanSQLiteDatabases walks the storage tree and returns the set of SQLite
// database files and the set of their transient sidecar files (-wal, -shm,
// -journal). The sidecars are excluded from a backup: the online .backup of each
// database already captures their committed contents, and a sidecar copied a
// moment later would not match the snapshot and could corrupt a restore.
func scanSQLiteDatabases(source string) (dbs map[string]bool, sidecars map[string]bool, err error) {
	dbs = map[string]bool{}
	sidecars = map[string]bool{}
	err = filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() && isSQLiteDatabase(path) {
			dbs[path] = true
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				sidecars[path+suffix] = true
			}
		}
		return nil
	})
	return dbs, sidecars, err
}

func snapshotStorage(source string, dest string, progress *backupProgress) (int, int, error) {
	dbs, sidecars, err := scanSQLiteDatabases(source)
	if err != nil {
		return 0, 0, err
	}
	files := 0
	sqliteFiles := 0
	err = filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dest, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			files++
			return os.Symlink(link, target)
		case entry.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			// Skip the transient sidecars of databases captured via .backup;
			// copying them separately would risk a corrupt restore.
			if sidecars[path] {
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			files++
			if dbs[path] {
				sqliteFiles++
				if err := backupSQLiteDatabase(path, target, info.Size(), progress); err != nil {
					return err
				}
				return copyFileMetadata(path, target)
			}
			return copyRegularFile(path, target, info.Mode().Perm())
		default:
			return nil
		}
	})
	return files, sqliteFiles, err
}

func isSQLiteDatabase(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	header := make([]byte, 16)
	if _, err := io.ReadFull(file, header); err != nil {
		return false
	}
	return string(header) == "SQLite format 3\x00"
}

func backupSQLiteDatabase(source string, dest string, size int64, progress *backupProgress) error {
	if _, err := commandOutputFunc(time.Second, "sqlite3", "-version"); err != nil {
		return fmt.Errorf("sqlite3 is required to back up SQLite database %s: %w", source, err)
	}
	progress.phase("snapshot "+filepath.Base(source), size)

	// sqlite3 .backup emits no progress, so estimate it by polling the growing
	// destination file against the source size while the command runs.
	done := make(chan struct{})
	var wg sync.WaitGroup
	if progress.enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(150 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					if info, err := os.Stat(dest); err == nil {
						progress.set(info.Size())
					}
				}
			}
		}()
	}
	_, err := commandOutputFunc(sqliteBackupTimeout(size), "sqlite3", source, ".backup "+sqliteString(dest))
	close(done)
	wg.Wait()
	if err != nil {
		return err
	}
	progress.set(size)
	return nil
}

func sqliteString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func copyRegularFile(source string, dest string, mode os.FileMode) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return copyFileMetadata(source, dest)
}

func copyFileMetadata(source string, dest string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if err := os.Chmod(dest, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(dest, info.ModTime(), info.ModTime())
}

func writeTarGz(sourceDir string, destPath string, progress *backupProgress) error {
	var total int64
	_ = filepath.WalkDir(sourceDir, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr == nil && entry.Type().IsRegular() {
			if info, err := entry.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	progress.phase("archive", total)

	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	gzipWriter := gzip.NewWriter(out)
	defer gzipWriter.Close()
	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	return filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		link := ""
		if entry.Type()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		if _, err := io.Copy(tarWriter, &progressReader{r: file, p: progress}); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	})
}

func restoreStorageBackup(appName string, storagePath string, backupPath string, noRestart bool, w io.Writer) (err error) {
	if _, err := os.Stat(backupPath); err != nil {
		return err
	}
	parent := filepath.Dir(storagePath)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	restoreDir, err := os.MkdirTemp(parent, ".restore-"+appName+"-*")
	if err != nil {
		return err
	}
	restoreDirMoved := false
	defer func() {
		if !restoreDirMoved {
			_ = os.RemoveAll(restoreDir)
		}
	}()
	if err := extractTarGz(backupPath, restoreDir); err != nil {
		return err
	}
	if err := chownStorage(restoreDir); err != nil {
		return err
	}

	stopped := []string{}
	if !noRestart {
		stopped, err = stopRunningAppContainers(appName)
		if err != nil {
			return err
		}
	}
	restartStoppedOnError := len(stopped) > 0
	defer func() {
		if err != nil && restartStoppedOnError {
			_ = startContainers(stopped)
		}
	}()

	previousPath := ""
	if _, err := os.Stat(storagePath); err == nil {
		previousPath = storagePath + ".pre-restore-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.Rename(storagePath, previousPath); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(restoreDir, storagePath); err != nil {
		if previousPath != "" {
			_ = os.Rename(previousPath, storagePath)
		}
		return err
	}
	restoreDirMoved = true

	writeCheck(w, appName, "restore", "ok", backupPath)
	if previousPath != "" {
		writeCheck(w, appName, "restore", "previous", previousPath)
	}
	if noRestart {
		writeCheck(w, appName, "restart", "skipped", "--no-restart")
		return nil
	}
	if len(stopped) == 0 {
		writeCheck(w, appName, "restart", "skipped", "-", "no running containers")
		return nil
	}
	if err := startContainers(stopped); err != nil {
		return err
	}
	restartStoppedOnError = false
	writeCheck(w, appName, "restart", "ok", strings.Join(stopped, ","))
	return nil
}

func extractTarGz(backupPath string, destDir string) error {
	file, err := os.Open(backupPath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeArchiveTarget(destDir, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tarReader); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			modTime := header.ModTime
			if !modTime.IsZero() {
				_ = os.Chtimes(target, modTime, modTime)
			}
		case tar.TypeSymlink:
			link := filepath.Clean(header.Linkname)
			if filepath.IsAbs(link) || link == ".." || strings.HasPrefix(link, ".."+string(filepath.Separator)) {
				return fmt.Errorf("unsafe symlink in backup: %s -> %s", header.Name, header.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported backup entry type %d for %s", header.Typeflag, header.Name)
		}
	}
}

func safeArchiveTarget(destDir string, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("unsafe absolute backup path: %s", name)
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe backup path: %s", name)
	}
	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return "", err
	}
	target := filepath.Join(destAbs, clean)
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(targetAbs, destAbs+string(filepath.Separator)) && targetAbs != destAbs {
		return "", fmt.Errorf("unsafe backup path: %s", name)
	}
	return targetAbs, nil
}
