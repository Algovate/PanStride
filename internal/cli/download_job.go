package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"panstride/internal/baidu"
	"panstride/internal/transfer"
)

type fileTransfer func(link, target string, version transfer.Version) (skipped bool, err error)

type downloadSource struct {
	path string
	id   int64
}

type downloadJob struct {
	drive             *baidu.Client
	transfer          fileTransfer
	out               io.Writer
	keepGoing         bool
	batch             bool
	saved             int
	skipped           int
	fileFailures      int
	directoryFailures int
}

func (j *downloadJob) run(ctx context.Context, source downloadSource, output string) error {
	if source.id > 0 {
		return j.finish(j.downloadID(ctx, source.id, output))
	}
	return j.finish(j.downloadPath(ctx, source.path, output))
}

func (j *downloadJob) downloadPath(ctx context.Context, remote, output string) error {
	if err := os.MkdirAll(output, 0755); err != nil {
		return err
	}
	if remote == "/" {
		return j.downloadDir(ctx, remote, output)
	}
	found := false
	err := j.drive.List(ctx, path.Dir(remote), func(entry *baidu.Entry) (bool, error) {
		if entry.Path == remote {
			found = true
			return true, j.downloadEntry(ctx, entry, output)
		}
		return false, nil
	})
	if err == nil && !found {
		return fmt.Errorf("remote path %q was not found; use panstride ls to check its parent directory %q", remote, path.Dir(remote))
	}
	return err
}

func (j *downloadJob) downloadDir(ctx context.Context, remote, output string) error {
	j.batch = true
	var childError error
	err := j.drive.List(ctx, remote, func(entry *baidu.Entry) (bool, error) {
		childError = j.downloadEntry(ctx, entry, output)
		return false, childError
	})
	if err != nil && childError == nil {
		return j.failed(ctx, remote, true, err)
	}
	return err
}

func (j *downloadJob) downloadEntry(ctx context.Context, entry *baidu.Entry, output string) error {
	if entry.Isdir == 1 {
		return j.downloadFolder(ctx, entry.Path, output)
	}
	return j.file(ctx, entry.Path, func() (bool, error) {
		meta, err := j.drive.FileMetadata(ctx, entry.FsID)
		if err != nil {
			return false, fmt.Errorf("get metadata for %q: %w", entry.Path, err)
		}
		if meta.Path != entry.Path || meta.Isdir != 0 {
			return false, fmt.Errorf("remote entry %q changed during lookup; retry the command", entry.Path)
		}
		if meta.ServerMtime == 0 {
			meta.ServerMtime = entry.ServerMtime
		}
		return j.downloadFile(ctx, meta, output)
	})
}

func (j *downloadJob) downloadFolder(ctx context.Context, remote, output string) error {
	j.batch = true
	target, err := localTarget(remote, output)
	if err != nil {
		return j.failed(ctx, remote, true, err)
	}
	if err := os.Mkdir(target, 0755); err != nil && !os.IsExist(err) {
		return j.failed(ctx, remote, true, err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		return j.failed(ctx, remote, true, err)
	}
	if !info.IsDir() {
		return j.failed(ctx, remote, true, fmt.Errorf("destination %q is not a directory", target))
	}
	return j.downloadDir(ctx, remote, target)
}

func (j *downloadJob) downloadID(ctx context.Context, id int64, output string) error {
	file, err := j.drive.FileMetadata(ctx, id)
	if err != nil {
		return fmt.Errorf("get metadata for file ID %d: %w", id, err)
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		return err
	}
	if file.Isdir == 1 {
		return j.downloadFolder(ctx, file.Path, output)
	}
	return j.file(ctx, file.Path, func() (bool, error) {
		return j.downloadFile(ctx, file, output)
	})
}

func (j *downloadJob) downloadFile(ctx context.Context, file baidu.FileMeta, output string) (bool, error) {
	target, err := localTarget(file.Path, output)
	if err != nil {
		return false, err
	}
	version := fileVersion(file)
	for attempt := 1; ; attempt++ {
		if file.Dlink == "" {
			return false, fmt.Errorf("Baidu returned no download link for %q", file.Path)
		}
		link, err := j.drive.AuthorizeLink(file.Dlink)
		if err != nil {
			return false, fmt.Errorf("%q: %w", file.Path, err)
		}

		skipped, err := j.transfer(link, target, version)
		if !errors.Is(err, transfer.ErrStalled) {
			return skipped, err
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if attempt > 1 {
			return false, fmt.Errorf("download stalled again for %q after one retry: %w", file.Path, err)
		}

		fmt.Fprintf(j.out, "%v for %q. Fetching a fresh link and retrying once.\n", err, file.Path)
		fresh, err := j.drive.FileMetadata(ctx, version.ID)
		if err != nil {
			return false, fmt.Errorf("refresh download link for %q: %w", file.Path, err)
		}
		if fresh.Path != file.Path || fresh.Isdir != 0 || fileVersion(fresh) != version {
			return false, fmt.Errorf("remote file changed during download: %q", file.Path)
		}
		file = fresh
	}
}

func fileVersion(file baidu.FileMeta) transfer.Version {
	return transfer.Version{ID: file.FsID, Size: file.Size, Modified: file.ServerMtime, MD5: baidu.NormalizeMD5(file.MD5)}
}

func localTarget(remote, output string) (string, error) {
	name, err := filepath.Localize(path.Base(remote))
	if err != nil {
		return "", fmt.Errorf("cannot use %q as a local filename: %w", remote, err)
	}
	if strings.EqualFold(name, transfer.StateDir) {
		return "", fmt.Errorf("cannot download %q: %s is reserved for partial downloads", remote, transfer.StateDir)
	}
	return filepath.Join(output, name), nil
}

func (j *downloadJob) file(ctx context.Context, remote string, download func() (bool, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if j.batch {
		fmt.Fprintf(j.out, "File %d: %q (%d finished)\n",
			j.saved+j.skipped+j.fileFailures+1, remote, j.saved+j.skipped)
	}
	skipped, err := download()
	if err != nil {
		return j.failed(ctx, remote, false, err)
	}
	if skipped {
		j.skipped++
	} else {
		j.saved++
	}
	return nil
}

func (j *downloadJob) failed(ctx context.Context, remote string, directory bool, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if directory {
		j.directoryFailures++
	} else {
		j.fileFailures++
	}
	if j.batch && j.keepGoing {
		fmt.Fprintf(j.out, "Failed %q: %v\n", remote, err)
		return nil
	}
	return err
}

func (j *downloadJob) finish(err error) error {
	if j.batch {
		fmt.Fprintf(j.out, "Summary: %d saved, %d skipped, failed files: %d, failed directories: %d.\n",
			j.saved, j.skipped, j.fileFailures, j.directoryFailures)
	}
	if err != nil {
		return err
	}
	if j.fileFailures+j.directoryFailures > 0 {
		return errors.New("download finished with failures")
	}
	return nil
}
