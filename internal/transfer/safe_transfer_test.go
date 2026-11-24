package transfer

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRejectsSameSizeConflict(t *testing.T) {
	target := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(target, []byte("evil"), 0600); err != nil {
		t.Fatal(err)
	}
	version := Version{ID: 12, Size: 4, MD5: fmt.Sprintf("%x", md5.Sum([]byte("data")))}
	_, err := storeFile(context.Background(), target, version, io.Discard, func(string, bool) error {
		t.Fatal("an existing unverified file must not be overwritten")
		return nil
	}, nil)
	if err == nil {
		t.Fatal("same length alone must not pass verification")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "evil" {
		t.Fatalf("existing file changed: %q, %v", got, err)
	}
}

func TestStoreIsolatesChangedRemoteVersion(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "file.txt")
	old := Version{ID: 12, Size: 4, Modified: 1}
	var oldPart string
	_, err := storeFile(context.Background(), target, old, io.Discard, func(part string, resume bool) error {
		oldPart = part
		if err := os.WriteFile(part, []byte("old"), 0600); err != nil {
			return err
		}
		if err := os.WriteFile(part+".aria2", nil, 0600); err != nil {
			return err
		}
		return context.Canceled
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected interrupted transfer: %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete file appeared at final path: %v", err)
	}

	current := Version{ID: 12, Size: 4, Modified: 2, MD5: fmt.Sprintf("%x", md5.Sum([]byte("new!")))}
	_, err = storeFile(context.Background(), target, current, io.Discard, func(part string, resume bool) error {
		if resume || part == oldPart {
			t.Fatal("changed remote version reused the previous partial file")
		}
		return os.WriteFile(part, []byte("new!"), 0600)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "new!" {
		t.Fatalf("verified bytes not published: %q, %v", got, err)
	}
}

func TestStoreRejectsWrongChecksumBeforePublishing(t *testing.T) {
	target := filepath.Join(t.TempDir(), "file.txt")
	version := Version{ID: 12, Size: 4, MD5: fmt.Sprintf("%x", md5.Sum([]byte("data")))}
	_, err := storeFile(context.Background(), target, version, io.Discard, func(part string, resume bool) error {
		return os.WriteFile(part, []byte("evil"), 0600)
	}, nil)
	if err == nil {
		t.Fatal("mis-matched checksum should fail")
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid file appeared at final path: %v", err)
	}
}

func TestStoreDoesNotOverwriteFileCreatedDuringTransfer(t *testing.T) {
	target := filepath.Join(t.TempDir(), "file.txt")
	version := Version{ID: 12, Size: 4, MD5: fmt.Sprintf("%x", md5.Sum([]byte("data")))}
	_, err := storeFile(context.Background(), target, version, io.Discard, func(part string, resume bool) error {
		if err := os.WriteFile(part, []byte("data"), 0600); err != nil {
			return err
		}
		return os.WriteFile(target, []byte("other"), 0600)
	}, nil)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected atomic publish to reject collision: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "other" {
		t.Fatalf("destination changed: %q, %v", got, err)
	}
}

func TestStoreLocksTargetWhileTransferRuns(t *testing.T) {
	target := filepath.Join(t.TempDir(), "file.txt")
	version := Version{ID: 12, Size: 4, Modified: 1}
	_, err := storeFile(context.Background(), target, version, io.Discard, func(part string, resume bool) error {
		_, second := storeFile(context.Background(), target, version, io.Discard, func(string, bool) error {
			t.Fatal("another transfer acquired the same target")
			return nil
		}, nil)
		if second == nil {
			t.Fatal("expected an active download to lock its target")
		}
		return os.WriteFile(part, []byte("data"), 0600)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestStoreUsesDownloadChecksumWhenCatalogDiffers(t *testing.T) {
	target := filepath.Join(t.TempDir(), "file.txt")
	content := []byte("data")
	version := Version{ID: 12, Size: 4, Modified: 1, MD5: fmt.Sprintf("%x", md5.Sum([]byte("catalog")))}
	digest := func(context.Context) (string, error) { return fmt.Sprintf("%x", md5.Sum(content)), nil }

	_, err := storeFile(context.Background(), target, version, io.Discard, func(part string, resume bool) error {
		return os.WriteFile(part, content, 0600)
	}, digest)
	if err != nil {
		t.Fatal(err)
	}
	skipped, err := storeFile(context.Background(), target, version, io.Discard, func(string, bool) error {
		t.Fatal("verified destination should have been skipped")
		return nil
	}, digest)
	if err != nil || !skipped {
		t.Fatalf("verified output not recognized: %t, %v", skipped, err)
	}
}

func TestStoreRetainsCompletedBytesIfAuthoritativeDigestUnavailable(t *testing.T) {
	target := filepath.Join(t.TempDir(), "file.txt")
	version := Version{ID: 12, Size: 4, Modified: 1, MD5: fmt.Sprintf("%x", md5.Sum([]byte("catalog")))}
	var staged string
	_, err := storeFile(context.Background(), target, version, io.Discard, func(part string, resume bool) error {
		staged = part
		return os.WriteFile(part, []byte("data"), 0600)
	}, func(context.Context) (string, error) { return "", errors.New("headers unavailable") })
	if err == nil {
		t.Fatal("expected verification failure")
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unverified data appeared at the final path: %v", err)
	}
	got, err := os.ReadFile(staged)
	if err != nil || string(got) != "data" {
		t.Fatalf("staged bytes were lost: %q, %v", got, err)
	}
	_, err = storeFile(context.Background(), target, version, io.Discard, func(string, bool) error {
		t.Fatal("completed staged data should be verified without transferring again")
		return nil
	}, func(context.Context) (string, error) { return fmt.Sprintf("%x", md5.Sum([]byte("data"))), nil })
	if err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(target)
	if err != nil || string(got) != "data" {
		t.Fatalf("staged bytes were not published: %q, %v", got, err)
	}
}
