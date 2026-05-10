package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	infoLog  *log.Logger
	errorLog *log.Logger
	file     *os.File
	mu       sync.Mutex
)

func Init(logDir string) error {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("creating log directory: %w", err)
	}

	logPath := filepath.Join(logDir, "app.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}

	file = f
	multiWriter := io.MultiWriter(os.Stdout, f)

	infoLog = log.New(multiWriter, "[INFO]  ", log.Ldate|log.Ltime)
	errorLog = log.New(multiWriter, "[ERROR] ", log.Ldate|log.Ltime)

	log.SetOutput(multiWriter)

	return nil
}

func Info(format string, args ...interface{}) {
	mu.Lock()
	defer mu.Unlock()
	if infoLog != nil {
		infoLog.Printf(format, args...)
	}
}

func Error(format string, args ...interface{}) {
	mu.Lock()
	defer mu.Unlock()
	if errorLog != nil {
		errorLog.Printf(format, args...)
	}
}

func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		file.Close()
	}
}

func ReadLogs(logDir string, lines int) ([]string, error) {
	logPath := filepath.Join(logDir, "app.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	allLines := splitLines(string(data))
	if lines > 0 && len(allLines) > lines {
		allLines = allLines[len(allLines)-lines:]
	}

	for i, j := 0, len(allLines)-1; i < j; i, j = i+1, j-1 {
		allLines[i], allLines[j] = allLines[j], allLines[i]
	}

	return allLines, nil
}

func RotateIfNeeded(logDir string) error {
	mu.Lock()
	defer mu.Unlock()

	logPath := filepath.Join(logDir, "app.log")
	info, err := os.Stat(logPath)
	if err != nil {
		return nil
	}

	if info.Size() < 10*1024*1024 {
		return nil
	}

	if file != nil {
		file.Close()
	}

	archivePath := filepath.Join(logDir, fmt.Sprintf("app-%s.log", time.Now().Format("2006-01-02-150405")))
	os.Rename(logPath, archivePath)

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	file = f

	multiWriter := io.MultiWriter(os.Stdout, f)
	infoLog = log.New(multiWriter, "[INFO]  ", log.Ldate|log.Ltime)
	errorLog = log.New(multiWriter, "[ERROR] ", log.Ldate|log.Ltime)
	log.SetOutput(multiWriter)

	return nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
