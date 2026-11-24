package transfer

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

const StateDir = ".panstride"

type Version struct {
	ID       int64
	Size     int64
	Modified int64
	MD5      string
}

func storeFile(ctx context.Context, target string, version Version, out io.Writer, download func(string, bool) error, probe digestProbe) (bool, error) {
	state := filepath.Join(filepath.Dir(target), StateDir)
	if err := os.Mkdir(state, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return false, err
	}
	info, err := os.Lstat(state)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("download state path %q is not a directory", state)
	}

	name := filepath.Base(target)
	targetKey := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
	lockPath := filepath.Join(state, targetKey+".lock")
	if _, err := regularFile(lockPath); err != nil {
		return false, err
	}
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		return false, err
	}
	if !locked {
		return false, fmt.Errorf("another PanStride process is downloading %q", target)
	}
	defer lock.Close()

	if legacy, err := regularFile(target + ".aria2"); err != nil {
		return false, err
	} else if legacy != nil {
		return false, fmt.Errorf("found an older aria2 download for %q; move it and its .aria2 file aside before retrying", target)
	}
	if existing, err := regularFile(target); err != nil {
		return false, err
	} else if existing != nil {
		if version.MD5 == "" {
			return false, fmt.Errorf("cannot verify existing file %q without a remote checksum", target)
		}
		matches, err := matchesVersion(ctx, target, existing, version, probe)
		if err != nil {
			return false, err
		}
		if !matches {
			return false, fmt.Errorf("existing file %q differs from the remote file", target)
		}
		fmt.Fprintf(out, "Already present and verified %q\n", target)
		return true, nil
	}

	keyData := fmt.Sprintf("%s:%d:%d:%d:%s", targetKey, version.ID, version.Size, version.Modified, version.MD5)
	resumable := version.Modified > 0 || version.MD5 != ""
	if !resumable {
		keyData += ":" + rand.Text()
	}
	part := filepath.Join(state, fmt.Sprintf("%x.part", sha256.Sum256([]byte(keyData))))
	if !resumable {
		defer os.Remove(part)
		defer os.Remove(part + ".aria2")
	}

	control, err := regularFile(part + ".aria2")
	if err != nil {
		return false, err
	}
	partial, err := regularFile(part)
	if err != nil {
		return false, err
	}
	if control != nil && partial == nil {
		return false, fmt.Errorf("download state for %q has a control file without its partial download", target)
	}
	if control == nil && partial != nil {
		matches, err := matchesVersion(ctx, part, partial, version, probe)
		if err != nil {
			return false, err
		}
		if matches && version.MD5 != "" {
			if err := publishFile(part, target); err != nil {
				return false, err
			}
			fmt.Fprintf(out, "Saved %q (verified staged download)\n", target)
			return false, nil
		}
		if err := os.Remove(part); err != nil {
			return false, err
		}
	}

	started := time.Now()
	if err := download(part, control != nil); err != nil {
		if errors.Is(err, context.Canceled) && resumable {
			if info, _ := regularFile(part + ".aria2"); info != nil {
				fmt.Fprintf(out, "Interrupted %q. Run the same command to resume.\n", target)
			}
		}
		return false, err
	}
	partial, err = regularFile(part)
	if err != nil {
		return false, err
	}
	if partial == nil {
		return false, fmt.Errorf("downloader did not produce a file for %q", target)
	}
	if version.MD5 != "" {
		fmt.Fprintf(out, "Verifying checksum for %q\n", target)
	}
	matches, err := matchesVersion(ctx, part, partial, version, probe)
	if err != nil {
		return false, err
	}
	if !matches {
		return false, errors.Join(fmt.Errorf("downloaded bytes for %q did not match the remote size or checksum", target), os.Remove(part))
	}
	if err := publishFile(part, target); err != nil {
		return false, err
	}
	fmt.Fprintf(out, "Saved %q (%s)\n", target, time.Since(started).Round(100*time.Millisecond))
	return false, nil
}

func regularFile(name string) (os.FileInfo, error) {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", name)
	}
	return info, nil
}

func matchesVersion(ctx context.Context, name string, info os.FileInfo, version Version, probe digestProbe) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if info.Size() != version.Size {
		return false, nil
	}
	if version.MD5 == "" {
		return true, nil
	}
	file, err := os.Open(name)
	if err != nil {
		return false, err
	}
	defer file.Close()
	hash := md5.New()
	if _, err := io.Copy(hash, cancelableReader{ctx: ctx, reader: file}); err != nil {
		return false, err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual == version.MD5 || probe == nil {
		return actual == version.MD5, nil
	}
	// Baidu's catalog can return a different digest from the download server.
	content, err := probe(ctx)
	if err != nil {
		return false, fmt.Errorf("verify file contents: %w", err)
	}
	return actual == content, nil
}

type cancelableReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r cancelableReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func publishFile(part, target string) error {
	// Linking publishes the verified file atomically and fails if the destination exists.
	if err := os.Link(part, target); err != nil {
		return fmt.Errorf("cannot publish %q without overwriting: %w", target, err)
	}
	if err := os.Remove(part); err != nil {
		return fmt.Errorf("saved %q but could not remove its staging file: %w", target, err)
	}
	return nil
}
