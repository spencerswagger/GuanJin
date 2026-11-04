package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func reqWithQuery(q string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/x"+q, nil)
}

func TestParsePageDefaults(t *testing.T) {
	q, err := ParsePage(reqWithQuery(""))
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if q.Limit != DefaultPageLimit {
		t.Fatalf("limit = %d, want %d", q.Limit, DefaultPageLimit)
	}
	if q.Cursor != "" {
		t.Fatalf("cursor = %q, want empty", q.Cursor)
	}
}

func TestParsePageLimitAndCursor(t *testing.T) {
	q, err := ParsePage(reqWithQuery("?limit=7&cursor=abc"))
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if q.Limit != 7 || q.Cursor != "abc" {
		t.Fatalf("q = %+v", q)
	}
	if _, err := ParsePage(reqWithQuery("?limit=200")); err != nil {
		t.Fatalf("limit=200 must be accepted: %v", err)
	}
}

func TestParsePageErrors(t *testing.T) {
	cases := []struct {
		query string
		want  error
	}{
		{"?limit=201", ErrPageSizeExceeded},
		{"?limit=0", ErrInvalidLimit},
		{"?limit=-3", ErrInvalidLimit},
		{"?limit=abc", ErrInvalidLimit},
	}
	for _, tc := range cases {
		if _, err := ParsePage(reqWithQuery(tc.query)); !errors.Is(err, tc.want) {
			t.Fatalf("%s: want %v, got %v", tc.query, tc.want, err)
		}
	}
}

func TestParsePageIgnoresDeprecatedParams(t *testing.T) {
	q, err := ParsePage(reqWithQuery("?page=3&page_size=999"))
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if q.Limit != DefaultPageLimit {
		t.Fatalf("deprecated page_size must be ignored, got %d", q.Limit)
	}
}

func TestParsePageNilRequest(t *testing.T) {
	q, err := ParsePage(nil)
	if err != nil || q.Limit != DefaultPageLimit {
		t.Fatalf("nil request = %+v, %v", q, err)
	}
}

func TestCodeOfPageError(t *testing.T) {
	cases := []struct {
		err  error
		want Code
	}{
		{ErrPageSizeExceeded, CodePageSizeExceeded},
		{ErrInvalidLimit, CodeInvalidParam},
		{errors.New("other"), ""},
		{nil, ""},
	}
	for i, tc := range cases {
		if got := CodeOfPageError(tc.err); got != tc.want {
			t.Fatalf("case %d: %q, want %q", i, got, tc.want)
		}
	}
}
