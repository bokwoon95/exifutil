package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

type PartitionCmd struct {
	FileRegexps     []*regexp.Regexp
	NumWorkers      int
	Verbose         bool
	DryRun          bool
	ReplaceIfExists bool
	Stdout          io.Writer
	Stderr          io.Writer
	logger          *slog.Logger
}

func PartitionCommand(args []string) (*PartitionCmd, error) {
	partitionCmd := &PartitionCmd{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	flagset := flag.NewFlagSet("", flag.ContinueOnError)
	flagset.IntVar(&partitionCmd.NumWorkers, "num-workers", 8, "Number of concurrent workers.")
	flagset.BoolVar(&partitionCmd.Verbose, "verbose", false, "Verbose output.")
	flagset.BoolVar(&partitionCmd.DryRun, "dry-run", false, "Print partition operations without executing.")
	flagset.BoolVar(&partitionCmd.ReplaceIfExists, "replace-if-exists", false, "If a file with the same name already exists in the date directory, replace it.")
	flagset.Func("file", "Include file regex. Can be repeated.", func(value string) error {
		r, err := compileRegexp(value)
		if err != nil {
			return err
		}
		partitionCmd.FileRegexps = append(partitionCmd.FileRegexps, r)
		return nil
	})
	err := flagset.Parse(args)
	if err != nil {
		return nil, err
	}
	logLevel := slog.LevelError
	if partitionCmd.Verbose {
		logLevel = slog.LevelInfo
	}
	partitionCmd.logger = slog.New(slog.NewTextHandler(partitionCmd.Stdout, &slog.HandlerOptions{
		AddSource: true,
		Level:     logLevel,
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			switch attr.Key {
			case slog.TimeKey:
				return slog.Attr{}
			case slog.SourceKey:
				source := attr.Value.Any().(*slog.Source)
				return slog.Any(slog.SourceKey, &slog.Source{
					Function: source.Function,
					File:     filepath.Base(source.File),
					Line:     source.Line,
				})
			default:
				return attr
			}
		},
	}))
	return partitionCmd, nil
}

func (partitionCmd *PartitionCmd) Run(ctx context.Context) error {
	type Exif struct {
		FileSize               string
		SubSecDateTimeOriginal string
		CreateDate             string
		TimeZone               string
	}
	var waitGroup sync.WaitGroup
	defer waitGroup.Wait()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	filePaths := make(chan string)
	for i := 0; i < partitionCmd.NumWorkers; i++ {
		exifToolCmd := exec.Command("exiftool", "-stay_open", "True", "-@", "-")
		setpgid(exifToolCmd)
		exifToolStdin, err := exifToolCmd.StdinPipe()
		if err != nil {
			return err
		}
		exifToolStdout, err := exifToolCmd.StdoutPipe()
		if err != nil {
			return err
		}
		exifToolStderr, err := exifToolCmd.StderrPipe()
		if err != nil {
			return err
		}
		go func() {
			_, _ = io.Copy(partitionCmd.Stderr, exifToolStderr)
		}()
		err = exifToolCmd.Start()
		if err != nil {
			return fmt.Errorf("%s: %w", exifToolCmd.String(), err)
		}
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			defer func() {
				_, err := io.WriteString(exifToolStdin, "-stay_open\n"+
					"False\n")
				if err != nil {
					partitionCmd.logger.Warn(err.Error())
				}
				stop(exifToolCmd)
			}()
			var buf bytes.Buffer
			reader := bufio.NewReader(exifToolStdout)
			for {
				select {
				case <-ctx.Done():
					return
				case filePath := <-filePaths:
					logger := partitionCmd.logger.With(slog.String("filePath", filePath))
					_, err := io.WriteString(exifToolStdin, "-json\n"+
						filePath+"\n"+
						"-execute\n")
					if err != nil {
						logger.Error(err.Error())
						break
					}
					buf.Reset()
					for {
						line, err := reader.ReadBytes('\n')
						if err != nil {
							if err == io.EOF {
								logger.Error("exiftool returned EOF prematurely")
								return
							}
							logger.Error(err.Error())
							return
						}
						if string(line) != "{ready}\n" {
							buf.Write(line)
							continue
						}
						break
					}
					var exifs []Exif
					err = json.Unmarshal(buf.Bytes(), &exifs)
					if err != nil {
						partitionCmd.logger.Error(err.Error(), slog.String("data", buf.String()))
						break
					}
					exif := exifs[0]
					var creationTime time.Time
					if exif.SubSecDateTimeOriginal != "" {
						creationTime, err = time.ParseInLocation("2006:01:02 15:04:05.000-07:00", exif.SubSecDateTimeOriginal, time.UTC)
						if err != nil {
							logger.Error(err.Error(), slog.String("SubSecDateTimeOriginal", exif.SubSecDateTimeOriginal))
							break
						}
					} else if exif.CreateDate != "" {
						creationTime, err = time.ParseInLocation("2006:01:02 15:04:05-07:00", exif.CreateDate+exif.TimeZone, time.UTC)
						if err != nil {
							logger.Error(err.Error(), slog.String("SubSecDateTimeOriginal", exif.SubSecDateTimeOriginal))
							break
						}
						creationTime = creationTime.Add(time.Duration(rand.IntN(1000)) * time.Millisecond)
					} else {
						logger.Error("unable to fetch file creation time", slog.String("data", buf.String()))
						break
					}
					dateDirPath := filepath.Join(filepath.Dir(filePath), creationTime.Format("2006-01-02"))
					newFilePath := filepath.Join(dateDirPath, filepath.Base(filePath))
					if partitionCmd.DryRun {
						b, err := json.Marshal(exif)
						if err != nil {
							logger.Warn(err.Error())
						}
						fmt.Fprintf(partitionCmd.Stdout, "%s => %s %s\n", filePath, newFilePath, string(b))
						break
					}
					err = os.MkdirAll(dateDirPath, 0755)
					if err != nil {
						logger.Error(err.Error(), slog.String("dateDirPath", dateDirPath))
						break
					}
					if partitionCmd.ReplaceIfExists {
						err := os.Rename(filePath, newFilePath)
						if err != nil {
							logger.Error(err.Error(), slog.String("newFilePath", newFilePath))
							break
						}
						logger.Info("moved file", slog.String("newFilePath", newFilePath))
						break
					}
					_, err = os.Stat(newFilePath)
					if err != nil {
						if !errors.Is(err, fs.ErrNotExist) {
							logger.Error(err.Error(), slog.String("name", newFilePath))
							break
						}
						err := os.Rename(filePath, newFilePath)
						if err != nil {
							logger.Error(err.Error(), slog.String("newFilePath", newFilePath))
							break
						}
						logger.Info("moved file", slog.String("newFilePath", newFilePath))
					} else {
						logger.Info("file already exists, skipping (use -replace-if-exists to replace it)", slog.String("newFilePath", newFilePath))
					}
				}
			}
		}()
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		dirEntries, err := os.ReadDir(cwd)
		if err != nil {
			return err
		}
		for _, dirEntry := range dirEntries {
			if dirEntry.IsDir() {
				continue
			}
			name := dirEntry.Name()
			for _, fileRegexp := range partitionCmd.FileRegexps {
				if fileRegexp.MatchString(name) {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case filePaths <- filepath.Join(cwd, name):
						break
					}
				}
			}
		}
	}
	return nil
}
