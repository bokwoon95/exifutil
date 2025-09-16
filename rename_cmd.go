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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
)

type RenameCmd struct {
	Roots           []string
	FileRegexps     []*regexp.Regexp
	NumWorkers      int
	Recursive       bool
	Verbose         bool
	DryRun          bool
	ReplaceIfExists bool
	Stdout          io.Writer
	Stderr          io.Writer
	logger          *slog.Logger
}

func RenameCommand(args []string) (*RenameCmd, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	renameCmd := &RenameCmd{
		Roots:  []string{cwd},
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	flagset := flag.NewFlagSet("", flag.ContinueOnError)
	flagset.IntVar(&renameCmd.NumWorkers, "num-workers", 8, "Number of concurrent workers.")
	flagset.BoolVar(&renameCmd.Recursive, "recursive", false, "Walk the roots recursively.")
	flagset.BoolVar(&renameCmd.Verbose, "verbose", false, "Verbose output.")
	flagset.BoolVar(&renameCmd.DryRun, "dry-run", false, "Print rename operations without executing.")
	flagset.BoolVar(&renameCmd.ReplaceIfExists, "replace-if-exists", false, "If a file with the new name already exists, replace it.")
	flagset.Func("root", "Specify an additional root directory to watch. Can be repeated.", func(value string) error {
		root, err := filepath.Abs(value)
		if err != nil {
			return err
		}
		renameCmd.Roots = append(renameCmd.Roots, root)
		return nil
	})
	flagset.Func("file", "Include file regex. Can be repeated.", func(value string) error {
		r, err := compileRegexp(value)
		if err != nil {
			return err
		}
		renameCmd.FileRegexps = append(renameCmd.FileRegexps, r)
		return nil
	})
	err = flagset.Parse(args)
	if err != nil {
		return nil, err
	}
	logLevel := slog.LevelError
	if renameCmd.Verbose {
		logLevel = slog.LevelInfo
	}
	renameCmd.logger = slog.New(slog.NewTextHandler(renameCmd.Stdout, &slog.HandlerOptions{
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
	return renameCmd, nil
}

func (renameCmd *RenameCmd) Run(ctx context.Context) error {
	var waitGroup sync.WaitGroup
	defer waitGroup.Wait()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	filePaths := make(chan string)
	for i := 0; i < renameCmd.NumWorkers; i++ {
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
			_, _ = io.Copy(renameCmd.Stderr, exifToolStderr)
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
					renameCmd.logger.Warn(err.Error())
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
					logger := renameCmd.logger.With(slog.String("filePath", filePath))
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
					exif := parseExif(logger, buf.Bytes())
					if exif.CreationTime.IsZero() {
						logger.Error("unable to fetch file creation time", slog.String("data", buf.String()))
						break
					}
					newFilePath := filepath.Join(filepath.Dir(filePath), exif.CreationTime.Format("2006-01-02T150405.000-0700") + filepath.Ext(filePath))
					if renameCmd.DryRun {
						b, err := json.Marshal(exif)
						if err != nil {
							logger.Warn(err.Error())
						}
						fmt.Fprintf(renameCmd.Stdout, "%s => %s %s\n", filePath, newFilePath, string(b))
						break
					}
					if renameCmd.ReplaceIfExists {
						err := os.Rename(filePath, newFilePath)
						if err != nil {
							logger.Error(err.Error(), slog.String("newFilePath", newFilePath))
							break
						}
						logger.Info("renamed file", slog.String("newFilePath", newFilePath))
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
						logger.Info("renamed file", slog.String("newFilePath", newFilePath))
					} else {
						logger.Info("file already exists, skipping (use -replace-if-exists to replace it)", slog.String("newFilePath", newFilePath))
					}
				}
			}
		}()
	}
	for _, root := range renameCmd.Roots {
		err := fs.WalkDir(os.DirFS(root), ".", func(path string, dirEntry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if dirEntry.IsDir() {
				if path != "." && !renameCmd.Recursive {
					return fs.SkipDir
				}
				return nil
			}
			name := dirEntry.Name()
			for _, fileRegexp := range renameCmd.FileRegexps {
				if fileRegexp.MatchString(name) {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case filePaths <- filepath.Join(root, path):
						break
					}
					return nil
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
