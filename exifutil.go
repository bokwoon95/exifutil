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

func parseExifs(logger *slog.Logger, data []byte) []Exif {
	type RawExif struct {
		FileSize               string
		SubSecDateTimeOriginal string
		CreateDate             string
		TimeZone               string
	}
	var rawExifs []RawExif
	err := json.Unmarshal(data, &rawExifs)
	if err != nil {
		logger.Error(err.Error(), slog.String("data", string(data)))
		return []Exif{}
	}
	exifs := make([]Exif, 0, len(rawExifs))
	for _, rawExif := range rawExifs {
		var exif Exif
		if rawExif.SubSecDateTimeOriginal != "" {
			if strings.Contains(rawExif.SubSecDateTimeOriginal, "+") || strings.Contains(rawExif.SubSecDateTimeOriginal, "-") {
				exif.CreationTime, err = time.ParseInLocation("2006:01:02 15:04:05.999-07:00", rawExif.SubSecDateTimeOriginal, time.UTC)
				if err != nil {
					logger.Error(err.Error(), slog.String("SubSecDateTimeOriginal", rawExif.SubSecDateTimeOriginal))
				}
			} else {
				exif.CreationTime, err = time.ParseInLocation("2006:01:02 15:04:05.999", rawExif.SubSecDateTimeOriginal, time.UTC)
				if err != nil {
					logger.Error(err.Error(), slog.String("SubSecDateTimeOriginal", rawExif.SubSecDateTimeOriginal))
				}
			}
		} else if rawExif.CreateDate != "" {
			exif.CreationTime, err = time.ParseInLocation("2006:01:02 15:04:05-07:00", rawExif.CreateDate+rawExif.TimeZone, time.UTC)
			if err != nil {
				logger.Error(err.Error(), slog.String("SubSecDateTimeOriginal", rawExif.SubSecDateTimeOriginal))
			}
			exif.CreationTime = exif.CreationTime.Add(time.Duration(rand.IntN(1000)) * time.Millisecond)
		}
		exifs = append(exifs, exif)
	}
	return exifs
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
