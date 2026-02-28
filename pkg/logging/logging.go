package logging

import (
	"fmt"
	"os"
	"strings"
)

func Info(msg string, args ...any) {
	write("INFO", msg, args...)
}

func Debug(msg string, args ...any) {
	write("DEBUG", msg, args...)
}

func Warn(msg string, args ...any) {
	write("WARN", msg, args...)
}

func Error(msg string, args ...any) {
	write("ERROR", msg, args...)
}

func ErrorPersist(msg string, args ...any) {
	write("ERROR", msg, args...)
}

func RecoverPanic(scope string, onRecover func()) {
	if r := recover(); r != nil {
		write("PANIC", scope, "error", r)
		if onRecover != nil {
			onRecover()
		}
	}
}

func write(level string, msg string, args ...any) {
	var b strings.Builder
	b.WriteString(level)
	b.WriteString(": ")
	b.WriteString(msg)
	if len(args) > 0 {
		for i := 0; i < len(args); i += 2 {
			if i+1 >= len(args) {
				b.WriteString(fmt.Sprintf(" %v", args[i]))
				break
			}
			b.WriteString(fmt.Sprintf(" %v=%v", args[i], args[i+1]))
		}
	}
	_, _ = fmt.Fprintln(os.Stderr, b.String())
}
