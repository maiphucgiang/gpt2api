package httpapi

import (
	"strconv"
	"time"
)

func randomID() string { return strconv.FormatInt(time.Now().UnixNano(), 10) }
