package binding

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	DefaultLimit = 50
	MaxLimit     = 100
)

var (
	ErrInvalidCursor = errors.New("binding: a cursor this history did not issue")
	ErrInvalidLimit  = fmt.Errorf("binding: limit must be from 1 to %d", MaxLimit)
)

type Page struct {
	Entries    []Entry
	NextCursor string
}

func List(sessions []Entry, cursor string, limit int) (Page, error) {
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 1 || limit > MaxLimit {
		return Page{}, ErrInvalidLimit
	}
	ordered := append([]Entry(nil), sessions...)
	sort.SliceStable(ordered, func(i, j int) bool { return before(ordered[i], ordered[j]) })
	start := 0
	if cursor != "" {
		after, err := decodeCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		start = sort.Search(len(ordered), func(i int) bool { return before(after, ordered[i]) })
	}
	end := min(start+limit, len(ordered))
	page := Page{Entries: ordered[start:end]}
	if end < len(ordered) {
		page.NextCursor = encodeCursor(ordered[end-1])
	}
	return page, nil
}

func before(a, b Entry) bool {
	if a.TimeMS != b.TimeMS {
		return a.TimeMS > b.TimeMS
	}
	return a.Record.SessionID < b.Record.SessionID
}

func encodeCursor(entry Entry) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(entry.TimeMS, 10) + ":" + entry.Record.SessionID))
}

func decodeCursor(cursor string) (Entry, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return Entry{}, ErrInvalidCursor
	}
	timeText, sessionID, found := strings.Cut(string(raw), ":")
	if !found || sessionID == "" {
		return Entry{}, ErrInvalidCursor
	}
	timeMS, err := strconv.ParseInt(timeText, 10, 64)
	if err != nil {
		return Entry{}, ErrInvalidCursor
	}
	return Entry{TimeMS: timeMS, Record: Record{SessionID: sessionID}}, nil
}
