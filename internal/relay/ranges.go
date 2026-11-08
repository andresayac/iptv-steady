package relay

import (
	"fmt"
	"strconv"
	"strings"
)

// byteRange is a single range requested by a viewer. Multiple ranges are ignored, as HTTP allows.
type byteRange struct {
	present bool
	start   int64
	end     int64
	suffix  int64
}

func parseRange(header string) byteRange {
	unbounded := byteRange{end: -1}
	spec, found := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !found || strings.Contains(spec, ",") {
		return unbounded
	}
	first, last, found := strings.Cut(spec, "-")
	if !found {
		return unbounded
	}
	if first == "" {
		suffix, err := strconv.ParseInt(last, 10, 64)
		if err != nil || suffix <= 0 {
			return unbounded
		}
		return byteRange{present: true, end: -1, suffix: suffix}
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return unbounded
	}
	parsed := byteRange{present: true, start: start, end: -1}
	if last != "" {
		end, err := strconv.ParseInt(last, 10, 64)
		if err != nil || end < start {
			return unbounded
		}
		parsed.end = end
	}
	return parsed
}

func (r byteRange) header() string {
	switch {
	case !r.present:
		return ""
	case r.suffix > 0:
		return fmt.Sprintf("bytes=-%d", r.suffix)
	}
	return rangeHeader(r.start, r.end)
}

func rangeHeader(start, end int64) string {
	if end < 0 {
		return fmt.Sprintf("bytes=%d-", start)
	}
	return fmt.Sprintf("bytes=%d-%d", start, end)
}

// continuesFile reports whether the viewer already relies on the layout of a specific file.
func (r byteRange) continuesFile() bool {
	return r.present && (r.start > 0 || r.suffix > 0)
}
