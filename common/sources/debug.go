package sources

import (
	"log"
	"os"
	"strings"
)

// verboseLogging gates the per-source protocol logging (request/response body
// dumps and per-call lend/borrow calculations). It is OFF by default so those
// high-volume logs don't bury the higher-level leverage-engine logs. Set
// LOG_SOURCES=1 (or true) to re-enable them when debugging a source.
var verboseLogging = func() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_SOURCES")))
	return v == "1" || v == "true" || v == "yes"
}()

// Verbose reports whether per-source protocol logging is enabled.
func Verbose() bool { return verboseLogging }

// Debugf logs only when LOG_SOURCES is set. Use it for the noisy
// protocol-payload and lend/borrow-math logging that otherwise covers up the
// leverage engine's progress.
func Debugf(format string, args ...any) {
	if verboseLogging {
		log.Printf(format, args...)
	}
}
