// Package logging is the one place both binaries write log lines through -
// leveled text lines (Debug/Info/Warning/Error/Fatal) plus a level-free
// Event line for the JSON access and probe records that must not carry a
// prefix. It exists mainly to add a caller tag (file:line) to every line
// without every call site having to know its own position - see
// callerInfo.
package logging

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// Log holds the level-specific writers Init builds. There is exactly one
// instance, held in the package-level logInstance below; nothing in this
// package is safe to use before Init has run at least once.
type Log struct {
	debugLogger   *log.Logger
	infoLogger    *log.Logger
	warningLogger *log.Logger
	errorLogger   *log.Logger
	fatalLogger   *log.Logger
	eventLogger   *log.Logger
}

// logInstance is the single, package-wide logger, initialized in Init.
var logInstance Log

// assertInstanceExists is called at the top of every logging function to
// guarantee Init has run. A caller that forgot to call Init gets a
// default-configured logger (stdout) rather than a nil-pointer panic the
// first time it tries to log anything.
func assertInstanceExists() {
	if (Log{}) == logInstance {
		// this error is fatal, as it does not depend on the user system, only
		// on the program correctness itself.
		Init("", true)
		log.Printf("Logging instance not initialized, initializing with defaults.")
	}
}

// callerInfo retrieves the filename and line number of the log-function-caller.
// we need this since as we proxy our logging over this module, the
// log.Lshortfile information is lost. This function retrieves basically the
// same information, just a little deeper in the callstack.
func callerInfo() string {
	_, file, line, ok := runtime.Caller(2) // 2 levels up the call stack to get the caller of Info function
	if !ok {
		return "unknown:0"
	}
	shortFile := filepath.Base(file) // Extract just the filename without the full path
	return shortFile + ":" + strconv.Itoa(line)
}

// Init initializes a globally usable, custom log instance. It expects a path
// where the log file should be placed. The path will be ignored if logToStdout
// is set to true.
func Init(log_path string, logToStdout bool) {
	var file *os.File

	if logToStdout {
		file = os.Stdout
	} else {
		// using filepath is important here, since path separators are OS dependant
		// and we don't know if the log_path ends with a trailing separator.
		filePath := filepath.Join(filepath.Clean(log_path), "log.txt")

		// try to find out if logpath + logfile exist
		if _, err := os.Stat(filePath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// file or path does not exists, create it
				if err := os.MkdirAll(log_path, os.ModePerm); err != nil {
					log.Printf(
						"FAILED TO CREATE LOGFILE AT: %s, due to this error: %s",
						filePath, err)
				}
			} else {
				// os.Stat failed, but not due to the file not existing. Reason unknown.
				log.Printf(
					"FAILED TO CHECK IF LOGFILE EXISTS: %s, due to this error: %s",
					filePath, err)
			}
		}
		var err error
		file, err = os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
		if err != nil {
			log.Printf("FAILED TO OPEN LOG FILE AT: %s, due to this error: %s", log_path, err)
		}
	}

	logInstance.debugLogger = log.New(file, "DEBG: ", log.Ldate|log.Ltime)
	logInstance.infoLogger = log.New(file, "INFO: ", log.Ldate|log.Ltime)
	logInstance.warningLogger = log.New(file, "WARN: ", log.Ldate|log.Ltime)
	logInstance.errorLogger = log.New(file, "ERRO: ", log.Ldate|log.Ltime)
	logInstance.fatalLogger = log.New(file, "FATL: ", log.Ldate|log.Ltime)
	// note: ERRO and FATL sounds dumb, but having the same length for every prefix
	// improves readability
	logInstance.eventLogger = log.New(file, "", 0)
}

// Debug writes to the log-file specified in the settings,
// using the 'DEBG:' specifier.
func Debug(format string, v ...any) {
	assertInstanceExists()
	caller := callerInfo()
	logInstance.debugLogger.Printf("%s: "+format, append([]interface{}{caller}, v...)...)
}

// Info writes to the log-file specified in the settings,
// using the 'INFO:' specifier.
func Info(format string, v ...any) {
	assertInstanceExists()
	caller := callerInfo()
	logInstance.infoLogger.Printf("%s: "+format, append([]interface{}{caller}, v...)...)
}

// Warning writes to the log-file specified in the settings,
// using the 'WARN:' specifier.
func Warning(format string, v ...any) {
	assertInstanceExists()
	caller := callerInfo()
	logInstance.warningLogger.Printf("%s: "+format, append([]interface{}{caller}, v...)...)
}

// Error writes to the log-file specified in the settings,
// using the 'ERRO:' specifier.
func Error(format string, v ...any) {
	assertInstanceExists()
	caller := callerInfo()
	logInstance.errorLogger.Printf("%s: "+format, append([]interface{}{caller}, v...)...)
}

// Event writes a single pre-formatted line to the same stream as the other
// loggers, with no level prefix, date, or caller tag. Callers own the entire
// line - used for the JSON access and probe records, which carry their own
// timestamp and must survive `jq` without a log prefix mangling the leading
// '{'.
func Event(line string) {
	assertInstanceExists()
	logInstance.eventLogger.Print(line)
}

// Fatal writes to the log-file specified in the settings,
// using the 'FATL:' specifier, and calls os.Exit(1) afterwards.
func Fatal(format string, v ...any) {
	assertInstanceExists()
	caller := callerInfo()
	logInstance.fatalLogger.Printf("%s: "+format, append([]interface{}{caller}, v...)...)
	os.Exit(1)
}
