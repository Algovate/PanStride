package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"panstride/internal/baidu"
)

type listingFormat struct {
	asJSON  bool
	showIDs bool
}

func writeListing(out io.Writer, format listingFormat, list func(baidu.VisitEntry) error) error {
	if !format.asJSON {
		return list(writeEntries(out, format.showIDs))
	}

	type row struct {
		ID    string `json:"id"`
		Path  string `json:"path"`
		Size  int64  `json:"size"`
		IsDir bool   `json:"is_dir"`
	}
	entries := make([]row, 0)
	if err := list(func(entry *baidu.Entry) (bool, error) {
		entries = append(entries, row{
			ID: strconv.FormatInt(entry.FsID, 10), Path: entry.Path,
			Size: entry.Size, IsDir: entry.Isdir == 1,
		})
		return false, nil
	}); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(entries)
}

func writeEntries(out io.Writer, showIDs bool) baidu.VisitEntry {
	return func(entry *baidu.Entry) (bool, error) {
		var size any = entry.Size
		if entry.Isdir == 1 {
			size = "dir"
		}
		if showIDs {
			_, err := fmt.Fprintf(out, "%d\t%v\t%s\n", entry.FsID, size, entry.Path)
			return false, err
		}
		_, err := fmt.Fprintf(out, "%v\t%s\n", size, entry.Path)
		return false, err
	}
}
