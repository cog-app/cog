package notes_test

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func jsonReader(v any) io.Reader {
	data, _ := json.Marshal(v)
	return strings.NewReader(string(data))
}
