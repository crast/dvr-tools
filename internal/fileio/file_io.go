package fileio

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/pkg/errors"
)

func Copy(ctx context.Context, sourceFile, destFile string) error {
	src, err := os.Open(sourceFile)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(destFile, os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	buf := make([]byte, 1024*1024)
	defer dst.Close()
	_, err = io.CopyBuffer(dst, src, buf)
	return err
}

func Move(ctx context.Context, sourceFile, destFile string) error {
	sourceFileAbs, err := filepath.Abs(sourceFile)
	if err != nil {
		return errors.Wrap(err, "cannot abs")
	}
	destFileAbs, err := filepath.Abs(destFile)
	if err != nil {
		return errors.Wrap(err, "cannot abs")
	}

	if sourceFile == destFile {
		return fmt.Errorf("cannot move, %s and %s are the same file", sourceFile, destFile)
	}
	fi, err := os.Stat(sourceFileAbs)
	if err != nil {
		return err
	}

	destDir := filepath.Dir(destFileAbs)
	destDirInfo, err := os.Stat(destDir)
	if err != nil {
		return err
	}

	fsnumSrc, ok1 := fsnum(fi)
	fsnumDest, ok2 := fsnum(destDirInfo)
	if ok1 && ok2 && fsnumSrc == fsnumDest {
		return os.Rename(sourceFileAbs, destFileAbs)
	}

	addOnFile := destFileAbs + ".tmp." + strconv.FormatInt(rand.Int63(), 16)

	if err := Copy(ctx, sourceFileAbs, addOnFile); err != nil {
		return err
	}
	if err = os.Rename(addOnFile, destFileAbs); err != nil {
		return err
	}

	return os.Remove(sourceFile)
}

func fsnum(info os.FileInfo) (uint64, bool) {
	ls, ok := info.Sys().(*syscall.Stat_t)
	if ok {
		return ls.Dev, true
	}
	return 0, false
}
