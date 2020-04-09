package jsonio

import (
	"encoding/json"
	"os"

	"github.com/pkg/errors"
)

func ReadFile(filename string, v interface{}) error {
	f, err := os.Open(filename)
	if err != nil {
		return errors.Wrap(err, "readjson open file")
	}
	defer f.Close()
	return json.NewDecoder(f).Decode(v)
}

func WriteFile(filename string, v interface{}) error {
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE, 0666)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "\t")
	err = enc.Encode(v)
	if err != nil {
		f.Close()
		return err
	}
	return f.Close()

}
