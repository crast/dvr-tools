package mediainfo

import (
	"context"
	"encoding/json"
	"log"
	"os/exec"

	"github.com/pkg/errors"
)

func Parse(ctx context.Context, filename string) (*MediaInfo, error) {
	cmd := exec.CommandContext(ctx, "mediainfo", "--Output=JSON", filename)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.Wrap(err, "could not get stdout pipe")
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.Wrap(err, "could not start mediainfo")
	}
	var mediaInfo MediaInfo
	if err := json.NewDecoder(stdout).Decode(&mediaInfo); err != nil {
		return nil, errors.Wrap(err, "could not decode JSON")
	}
	if err = cmd.Wait(); err != nil {
		log.Fatal(err)
	}
	stdout.Close()
	return &mediaInfo, nil
}
