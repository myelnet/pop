package cli

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"runtime"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var loggingLevels = map[string]zerolog.Level{
	zerolog.TraceLevel.String(): zerolog.TraceLevel, // trace
	zerolog.DebugLevel.String(): zerolog.DebugLevel, // debug
	zerolog.InfoLevel.String():  zerolog.InfoLevel,  // info (default)
}

// LoggerHook displays the file & line the log comes from
type LoggerHook struct{}

func (h LoggerHook) Run(e *zerolog.Event, level zerolog.Level, msg string) {
	if _, file, line, ok := runtime.Caller(3); ok {
		e.Str("file", path.Base(file)).Int("line", line)
	}
}

func setupLogger(logDir string, logLevel string) error {

	loggingLevel, ok := loggingLevels[logLevel]
	if !ok {
		return fmt.Errorf("logging level [%s] does not exist", logLevel)
	}

	zerolog.SetGlobalLevel(loggingLevel)

	output := zerolog.ConsoleWriter{Out: os.Stderr}

	if loggingLevel < zerolog.InfoLevel {
		output.TimeFormat = time.RFC3339
	}

	if logDir != "" {
		// create log-dir, make sure you have correct permissions to do so
		err := os.MkdirAll(logDir, os.ModePerm)
		if err != nil {
			return err
		}

		// timestamped log
		t := time.Now().UTC()
		tempFile, err := ioutil.TempFile(logDir, t.Format("2006-01-02T150405")+".*.log")
		if err != nil {
			return err
		}

		// log to file as well as console in the same format
		fileLogger := output
		fileLogger.Out = tempFile

		multi := zerolog.MultiLevelWriter(output, fileLogger)
		log.Logger = zerolog.New(multi).With().Timestamp().Logger()

		log.Info().Msg(fmt.Sprintf("Created log file at %s", tempFile.Name()))

	} else {
		log.Logger = log.Output(output)
	}

	if loggingLevel < zerolog.InfoLevel {
		log.Logger = log.Hook(LoggerHook{})
	}

	return nil

}
