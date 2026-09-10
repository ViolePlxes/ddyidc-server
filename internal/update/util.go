package update

import (
	"strconv"
	"strings"
)

func formatID(id int64) string { return strconv.FormatInt(id, 10) }

type taskLog struct{ lines []string }

func (l *taskLog) add(line string) {
	line = strings.TrimSpace(sanitizeOutput(line))
	if line != "" {
		l.lines = append(l.lines, line)
	}
}

func (l *taskLog) String() string {
	value := strings.Join(l.lines, "\n")
	if len(value) > 128*1024 {
		value = value[len(value)-128*1024:]
	}
	return value
}
