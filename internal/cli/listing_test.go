package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"panstride/internal/baidu"
)

func TestJSONListingPreservesLargeIDsAndSpecialPaths(t *testing.T) {
	var out strings.Builder
	path := "/资料/有\t制表符和\n换行"
	err := writeListing(&out, listingFormat{asJSON: true}, func(visit baidu.VisitEntry) error {
		_, err := visit(&baidu.Entry{FsID: 9007199254740993, Path: path, Size: 8})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		ID   string `json:"id"`
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal([]byte(out.String()), &entries); err != nil || len(entries) != 1 ||
		entries[0].ID != "9007199254740993" || entries[0].Path != path || entries[0].Size != 8 {
		t.Fatalf("unexpected listing %q, %v", out.String(), err)
	}
}

func TestJSONListingDoesNotPrintPartialResults(t *testing.T) {
	var out strings.Builder
	want := errors.New("next page failed")
	err := writeListing(&out, listingFormat{asJSON: true}, func(visit baidu.VisitEntry) error {
		visit(&baidu.Entry{FsID: 42, Path: "/file"})
		return want
	})
	if !errors.Is(err, want) || out.Len() != 0 {
		t.Fatalf("partial JSON %q, error %v", out.String(), err)
	}
}
