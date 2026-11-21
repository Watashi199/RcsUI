// Package rsync implémente le moteur rsync : construction de l'argv, parsing
// de la sortie (--info=progress2, --out-format) et interprétation des codes de retour.
package rsync

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ItemPrefix est le préfixe placé par --out-format pour distinguer les items
// itemisés des autres messages rsync sur stdout.
const ItemPrefix = "::I "

// OutFormat est la valeur passée à --out-format. Champs : %i (11 car.), %l (taille), %n (nom).
const OutFormat = ItemPrefix + "%i %l %n"

// LineKind classe une ligne de sortie stdout.
type LineKind int

const (
	LineOther LineKind = iota
	LineItem
	LineProgress
	LineStat
	LineBlank
)

// Op est l'opération décrite par un item itemisé.
type Op string

const (
	OpTransfer Op = "transfer" // '<' ou '>'
	OpCreate   Op = "create"   // 'c' (création locale : dossier, lien...)
	OpHardlink Op = "hardlink" // 'h'
	OpAttrs    Op = "attrs"    // '.' : attributs seulement
	OpDelete   Op = "delete"   // '*deleting'
	OpMessage  Op = "message"  // autre '*'
)

// Item est un changement itemisé (une ligne --itemize-changes).
type Item struct {
	Op    Op     `json:"op"`
	Type  string `json:"kind"`  // f, d, L, D, S
	Flags string `json:"flags"` // les 11 caractères bruts de %i
	Size  int64  `json:"size"`
	Path  string `json:"path"` // décodé (\#ooo) et UTF-8 valide
}

// ProgressLine est une ligne --info=progress2 décodée.
type ProgressLine struct {
	Bytes    int64
	Percent  int
	SpeedBps float64
	ETA      time.Duration
	HasChk   bool  // la ligne contient "(xfr#N, xx-chk=R/T)"
	Xfr      int64 // fichiers transférés
	Remain   int64 // R de xx-chk
	Total    int64 // T de xx-chk
	Exact    bool  // to-chk (liste complète) vs ir-chk (récursion incrémentale)
}

// Stat est une ligne de statistiques (--info=stats2) "Clé: valeur".
type Stat struct {
	Key   string
	Value int64
}

// Line est le résultat de la classification d'une ligne.
type Line struct {
	Kind     LineKind
	Text     string // ligne brute (sans \r/\n), pour les logs
	Item     Item
	Progress ProgressLine
	Stat     Stat
}

var progressRe = regexp.MustCompile(
	// rsync ≤ 3.2 : "1238099  50%  19.38MB/s" ; rsync 3.5 avec --no-human-readable :
	// "1238099B  50% 20507437B/s" (suffixe B sur les octets, débit brut en B/s).
	`^\s*([\d,]+)B?\s+(\d{1,3})%\s+([\d.,]+)([kMGT]?B)/s\s+(\d+):(\d{2}):(\d{2})` +
		`(?:\s+\(xfr#(\d+),\s+(ir|to)-chk=(\d+)/(\d+)\))?\s*$`)

var statRe = regexp.MustCompile(`^(Number of [a-z ]*files(?: transferred)?|Total file size|Total transferred file size|Total bytes sent|Total bytes received|Literal data|Matched data): ([\d,]+)`)

// ParseLine classe une ligne de stdout rsync.
func ParseLine(s string) Line {
	l := Line{Text: s}
	if strings.TrimSpace(s) == "" {
		l.Kind = LineBlank
		return l
	}
	if strings.HasPrefix(s, ItemPrefix) {
		if it, ok := parseItem(s[len(ItemPrefix):]); ok {
			l.Kind, l.Item = LineItem, it
			return l
		}
	}
	if m := progressRe.FindStringSubmatch(s); m != nil {
		l.Kind = LineProgress
		l.Progress = parseProgress(m)
		return l
	}
	if m := statRe.FindStringSubmatch(s); m != nil {
		l.Kind = LineStat
		l.Stat = Stat{Key: m[1], Value: atoi(m[2])}
		return l
	}
	l.Kind = LineOther
	return l
}

func parseItem(s string) (Item, bool) {
	// %i fait 11 caractères (ex. ">f+++++++++" ou "*deleting  "), suivi d'un espace.
	if len(s) < 14 || s[11] != ' ' {
		return Item{}, false
	}
	flags := s[:11]
	rest := s[12:]
	sp := strings.IndexByte(rest, ' ')
	if sp <= 0 {
		return Item{}, false
	}
	size, err := strconv.ParseInt(rest[:sp], 10, 64)
	if err != nil {
		return Item{}, false
	}
	it := Item{Flags: flags, Size: size, Path: DecodeName(rest[sp+1:])}
	switch flags[0] {
	case '<', '>':
		it.Op = OpTransfer
	case 'c':
		it.Op = OpCreate
	case 'h':
		it.Op = OpHardlink
	case '.':
		it.Op = OpAttrs
	case '*':
		if strings.HasPrefix(flags, "*deleting") {
			it.Op = OpDelete
		} else {
			it.Op = OpMessage
		}
	default:
		return Item{}, false
	}
	switch {
	case it.Op != OpDelete && it.Op != OpMessage:
		it.Type = string(flags[1])
	case strings.HasSuffix(it.Path, "/"):
		it.Type = "d"
	default:
		it.Type = "f"
	}
	return it, true
}

func parseProgress(m []string) ProgressLine {
	p := ProgressLine{Bytes: atoi(m[1])}
	p.Percent, _ = strconv.Atoi(m[2])
	speed, _ := strconv.ParseFloat(strings.ReplaceAll(m[3], ",", ""), 64)
	// rsync exprime le débit en base 1024 (progress.c).
	switch m[4] {
	case "kB":
		speed *= 1 << 10
	case "MB":
		speed *= 1 << 20
	case "GB":
		speed *= 1 << 30
	case "TB":
		speed *= 1 << 40
	}
	p.SpeedBps = speed
	h, _ := strconv.Atoi(m[5])
	mi, _ := strconv.Atoi(m[6])
	se, _ := strconv.Atoi(m[7])
	p.ETA = time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute + time.Duration(se)*time.Second
	if m[8] != "" {
		p.HasChk = true
		p.Xfr = atoi(m[8])
		p.Exact = m[9] == "to"
		p.Remain = atoi(m[10])
		p.Total = atoi(m[11])
	}
	return p
}

func atoi(s string) int64 {
	v, _ := strconv.ParseInt(strings.ReplaceAll(s, ",", ""), 10, 64)
	return v
}

// DecodeName décode les échappements \#ooo (octal) que rsync applique aux
// caractères non imprimables, puis garantit une chaîne UTF-8 valide.
func DecodeName(s string) string {
	if !strings.Contains(s, `\#`) {
		return strings.ToValidUTF8(s, "�")
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 < len(s) && s[i+1] == '#' &&
			isOctal(s[i+2]) && isOctal(s[i+3]) && isOctal(s[i+4]) {
			b.WriteByte((s[i+2]-'0')<<6 | (s[i+3]-'0')<<3 | (s[i+4] - '0'))
			i += 4
			continue
		}
		b.WriteByte(s[i])
	}
	return strings.ToValidUTF8(b.String(), "�")
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// ScanCRLF est une bufio.SplitFunc qui coupe sur '\r' OU '\n' (progress2
// réécrit sa ligne avec '\r'). Les lignes vides sont rendues ; l'appelant les
// ignore via LineBlank.
func ScanCRLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// NewScanner retourne un scanner configuré pour la sortie rsync (lignes ≤ 1 MiB).
func NewScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	sc.Split(ScanCRLF)
	return sc
}
