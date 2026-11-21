package runner

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"rcsui/internal/engine"
	"rcsui/internal/events"
)

// LogRecord est une ligne du fichier de log NDJSON d'un run.
type LogRecord struct {
	T   int64           `json:"t"`
	S   string          `json:"s"`
	Lvl engine.LogLevel `json:"lvl"`
	Msg string          `json:"msg"`
}

// LogBatch est le contenu d'un événement run.log.
type LogBatch struct {
	RunID      string      `json:"runId"`
	FromOffset int64       `json:"fromOffset"`
	ToOffset   int64       `json:"toOffset"`
	Lines      []LogRecord `json:"lines"`
}

// ItemBatch est le contenu d'un événement run.item.
type ItemBatch struct {
	RunID string        `json:"runId"`
	Items []engine.Item `json:"items"`
	Total int64         `json:"total"`
}

// runSink implémente engine.Sink pour un run : écriture des fichiers NDJSON,
// diffusion par lots vers le broker, relais de la progression.
type runSink struct {
	runID  string
	jobID  int64
	broker *events.Broker
	onProg func(engine.Progress)

	mu        sync.Mutex
	logF      *os.File
	logW      *bufio.Writer
	itemF     *os.File
	itemW     *bufio.Writer
	offset    int64
	pendFrom  int64
	pending   []LogRecord
	pendItems []engine.Item
	items     int64
	closed    bool
	stop      chan struct{}
	stopped   chan struct{}
}

// LogPaths retourne les chemins (relatifs au dossier de logs) des fichiers d'un run.
func LogPaths(runID string, t time.Time) (logRel, itemsRel string) {
	dir := filepath.Join(t.UTC().Format("2006"), t.UTC().Format("01"))
	return filepath.Join(dir, runID+".ndjson"), filepath.Join(dir, runID+".items.ndjson")
}

func newRunSink(logDir, logRel, itemsRel, runID string, jobID int64, b *events.Broker, onProg func(engine.Progress)) (*runSink, error) {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(logDir, logRel)), 0o750); err != nil {
		return nil, err
	}
	lf, err := os.OpenFile(filepath.Join(logDir, logRel), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	itf, err := os.OpenFile(filepath.Join(logDir, itemsRel), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		lf.Close()
		return nil, err
	}
	s := &runSink{runID: runID, jobID: jobID, broker: b, onProg: onProg,
		logF: lf, logW: bufio.NewWriterSize(lf, 64*1024), itemF: itf, itemW: bufio.NewWriterSize(itf, 64*1024),
		stop: make(chan struct{}), stopped: make(chan struct{})}
	if st, err := lf.Stat(); err == nil {
		s.offset = st.Size()
	}
	s.pendFrom = s.offset
	go s.flusher()
	return s, nil
}

func (s *runSink) OnProgress(p engine.Progress) {
	if s.onProg != nil {
		s.onProg(p)
	}
}

func (s *runSink) OnLog(l engine.LogLine) {
	if l.Time.IsZero() {
		l.Time = time.Now()
	}
	rec := LogRecord{T: l.Time.UnixMilli(), S: l.Stream, Lvl: l.Level, Msg: l.Msg}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	n, _ := s.logW.Write(append(b, '\n'))
	s.offset += int64(n)
	s.pending = append(s.pending, rec)
}

func (s *runSink) OnItem(it engine.Item) {
	if it.Time.IsZero() {
		it.Time = time.Now()
	}
	b, err := json.Marshal(it)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	_, _ = s.itemW.Write(append(b, '\n'))
	s.items++
	if len(s.pendItems) < 200 { // seul un échantillon est diffusé en direct
		s.pendItems = append(s.pendItems, it)
	}
}

// flusher vide les buffers et publie les lots toutes les 250 ms.
func (s *runSink) flusher() {
	defer close(s.stopped)
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.flush()
		case <-s.stop:
			s.flush()
			return
		}
	}
}

func (s *runSink) flush() {
	s.mu.Lock()
	_ = s.logW.Flush()
	_ = s.itemW.Flush()
	lines, from, to := s.pending, s.pendFrom, s.offset
	items, total := s.pendItems, s.items
	s.pending, s.pendFrom, s.pendItems = nil, s.offset, nil
	s.mu.Unlock()
	if len(lines) > 0 {
		s.broker.PublishRun(s.runID, s.jobID, events.TypeRunLog, LogBatch{RunID: s.runID, FromOffset: from, ToOffset: to, Lines: lines}, false)
	}
	if len(items) > 0 {
		s.broker.PublishRun(s.runID, s.jobID, events.TypeRunItem, ItemBatch{RunID: s.runID, Items: items, Total: total}, false)
	}
}

// Close vide et ferme les fichiers ; retourne la taille du log.
func (s *runSink) Close() int64 {
	s.mu.Lock()
	if s.closed {
		defer s.mu.Unlock()
		return s.offset
	}
	s.mu.Unlock()
	close(s.stop)
	<-s.stopped
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	_ = s.logW.Flush()
	_ = s.itemW.Flush()
	_ = s.logF.Close()
	_ = s.itemF.Close()
	return s.offset
}
