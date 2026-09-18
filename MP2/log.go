package main

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

var (
	logMu  sync.Mutex
	logOut io.Writer = os.Stdout
)

// Opens the local log file and mirrors every event to it and the terminal
func InitLogger(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	logMu.Lock()
	logOut = io.MultiWriter(os.Stdout, f)
	logMu.Unlock()
	return nil
}

// Stamps each event with local time so logs from different VMs can be lined up by grep
func LogEvent(format string, args ...any) {
	line := fmt.Sprintf("%s %s\n", time.Now().Format("2006-01-02 15:04:05.000"), fmt.Sprintf(format, args...))
	logMu.Lock()
	defer logMu.Unlock()
	io.WriteString(logOut, line)
}
