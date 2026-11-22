package rsync

import (
	"fmt"

	"rcsui/internal/engine"
)

// ExitInfo décrit l'interprétation d'un code de retour rsync (man rsync, EXIT VALUES).
type ExitInfo struct {
	Outcome   engine.Outcome
	Retryable bool
	Message   string
}

// Messages en anglais, comme toutes les erreurs renvoyées par l'API (l'interface
// traduit ses propres textes, pas ceux du serveur). Libellés de man rsync.
var exitMessages = map[int]string{
	0:   "success",
	1:   "syntax or usage error",
	2:   "protocol incompatibility (remote rsync version?)",
	3:   "errors selecting input/output files, dirs",
	4:   "requested action not supported",
	5:   "error starting client-server protocol",
	10:  "error in socket I/O",
	11:  "error in file I/O",
	12:  "error in rsync protocol data stream",
	13:  "errors with program diagnostics",
	14:  "error in IPC code",
	15:  "sibling process crashed",
	16:  "sibling process terminated abnormally",
	19:  "received SIGUSR1",
	20:  "received SIGINT, SIGTERM, or SIGHUP",
	21:  "some error returned by waitpid()",
	22:  "error allocating core memory buffers",
	23:  "partial transfer due to error",
	24:  "partial transfer due to vanished source files",
	25:  "the --max-delete limit stopped deletions",
	30:  "timeout in data send/receive",
	35:  "timeout waiting for daemon connection",
	255: "SSH connection error",
}

// InterpretExit traduit un code de retour en issue de run.
//   - cancelRequested : l'utilisateur a demandé l'annulation (le code 20 devient "cancelled").
//   - vanishedOK : le code 24 est considéré comme un succès (réglage rsync_vanished_is_success).
func InterpretExit(code int, cancelRequested, vanishedOK bool) ExitInfo {
	msg, ok := exitMessages[code]
	if !ok {
		msg = fmt.Sprintf("unknown exit code %d", code)
	}
	info := ExitInfo{Message: msg}
	if cancelRequested {
		// Après SIGTERM, rsync sort typiquement en 20, mais un kill -9 ou une
		// course peut produire un autre code : l'intention de l'utilisateur prime.
		info.Outcome = engine.OutcomeCancelled
		return info
	}
	switch code {
	case 0:
		info.Outcome = engine.OutcomeSucceeded
	case 24:
		if vanishedOK {
			info.Outcome = engine.OutcomeSucceeded
		} else {
			info.Outcome = engine.OutcomePartial
		}
	case 23, 25:
		info.Outcome = engine.OutcomePartial
	case 5, 10, 12, 30, 35, 255:
		info.Outcome = engine.OutcomeFailed
		info.Retryable = true
	default:
		info.Outcome = engine.OutcomeFailed
	}
	return info
}
