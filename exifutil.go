package main

import (
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type Exif struct {
	CreationTime time.Time
}

func parseExif(logger *slog.Logger, data []byte) Exif {
	type RawExif struct {
		FileSize               string
		SubSecDateTimeOriginal string
		CreateDate             string
		TimeZone               string
	}
	var exif Exif
	var rawExifs []RawExif
	err := json.Unmarshal(data, &rawExifs)
	if err != nil {
		logger.Error(err.Error(), slog.String("data", string(data)))
		return exif
	}
	rawExif := rawExifs[0]
	if rawExif.SubSecDateTimeOriginal != "" {
		exif.CreationTime, err = time.ParseInLocation("2006:01:02 15:04:05.000-07:00", rawExif.SubSecDateTimeOriginal, time.UTC)
		if err != nil {
			logger.Error(err.Error(), slog.String("SubSecDateTimeOriginal", rawExif.SubSecDateTimeOriginal))
		}
	} else if rawExif.CreateDate != "" {
		exif.CreationTime, err = time.ParseInLocation("2006:01:02 15:04:05-07:00", rawExif.CreateDate+rawExif.TimeZone, time.UTC)
		if err != nil {
			logger.Error(err.Error(), slog.String("SubSecDateTimeOriginal", rawExif.SubSecDateTimeOriginal))
		}
		exif.CreationTime = exif.CreationTime.Add(time.Duration(rand.IntN(1000)) * time.Millisecond)
	}
	return exif
}

func compileRegexp(pattern string) (*regexp.Regexp, error) {
	n := strings.Count(pattern, ".")
	if n == 0 {
		return regexp.Compile(pattern)
	}
	if strings.HasPrefix(pattern, "./") && len(pattern) > 2 {
		pattern = pattern[2:]
	}
	var b strings.Builder
	b.Grow(len(pattern) + n)
	j := 0
	for j < len(pattern) {
		prev, _ := utf8.DecodeLastRuneInString(b.String())
		curr, width := utf8.DecodeRuneInString(pattern[j:])
		next, _ := utf8.DecodeRuneInString(pattern[j+width:])
		j += width
		if prev != '\\' && curr == '.' && (('a' <= next && next <= 'z') || ('A' <= next && next <= 'Z')) {
			b.WriteString("\\.")
		} else {
			b.WriteRune(curr)
		}
	}
	return regexp.Compile(b.String())
}
