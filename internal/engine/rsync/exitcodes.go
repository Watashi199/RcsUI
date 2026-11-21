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

var exitMessages = map[int]string{
	0:   "succès",
	1:   "erreur de syntaxe ou d'usage",
	2:   "incompatibilité de protocole (version rsync distante ?)",
	3:   "erreur de sélection des fichiers/dossiers d'entrée-sortie",
	4:   "action non supportée",
	5:   "erreur au démarrage du protocole client-serveur",
	10:  "erreur d'E/S socket",
	11:  "erreur d'E/S fichier",
	12:  "erreur dans le flux du protocole rsync",
	13:  "erreur de diagnostic",
	14:  "erreur IPC",
	15:  "processus frère planté",
	16:  "processus frère tué par un signal",
	19:  "SIGUSR1 reçu",
	20:  "SIGINT, SIGTERM ou SIGHUP reçu",
	21:  "erreur de waitpid()",
	22:  "erreur d'allocation mémoire",
	23:  "transfert partiel dû à des erreurs",
	24:  "transfert partiel : des fichiers source ont disparu",
	25:  "la limite --max-delete a stoppé des suppressions",
	30:  "timeout en émission/réception",
	35:  "timeout en attente de connexion au démon",
	255: "erreur de connexion SSH",
}

// InterpretExit traduit un code de retour en issue de run.
//   - cancelRequested : l'utilisateur a demandé l'annulation (le code 20 devient "cancelled").
//   - vanishedOK : le code 24 est considéré comme un succès (réglage rsync_vanished_is_success).
func InterpretExit(code int, cancelRequested, vanishedOK bool) ExitInfo {
	msg, ok := exitMessages[code]
	if !ok {
		msg = fmt.Sprintf("code de sortie inconnu %d", code)
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
