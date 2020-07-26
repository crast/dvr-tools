package logic

import (
	"context"
	"io/ioutil"
	"net/http"

	"github.com/sirupsen/logrus"
)

func httpGet(ctx context.Context, path string) ([]byte, error) {
	logrus.Debug("about to get ", PlexServer+path)
	req, err := http.NewRequestWithContext(ctx, "GET", PlexServer+path, nil)
	if err != nil {
		return nil, err
	}
	if Token != "" {
		req.Header.Add("X-Plex-Token", Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return ioutil.ReadAll(resp.Body)
}
