package rsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rcsui/internal/engine"
)

func TestParseItem(t *testing.T) {
	cases := []struct {
		in   string
		op   Op
		typ  string
		size int64
		path string
	}{
		{"::I >f+++++++++ 30000000 big.bin", OpTransfer, "f", 30000000, "big.bin"},
		{"::I cL+++++++++ 8 link", OpCreate, "L", 8, "link"},
		{`::I >f+++++++++ 1 new\#012line`, OpTransfer, "f", 1, "new\nline"},
		{"::I >f+++++++++ 0 sub dir/file 0.bin", OpTransfer, "f", 0, "sub dir/file 0.bin"},
		{"::I *deleting   0 stale/", OpDelete, "d", 0, "stale/"},
		{"::I *deleting   0 gone.txt", OpDelete, "f", 0, "gone.txt"},
		{"::I .f...p..... 111000 sub dir/file 3.bin", OpAttrs, "f", 111000, "sub dir/file 3.bin"},
		{"::I .d..t...... 4096 ./", OpAttrs, "d", 4096, "./"},
		{`::I >f+++++++++ 1 unicod\#303\#251.txt`, OpTransfer, "f", 1, "unicodé.txt"},
	}
	for _, c := range cases {
		l := ParseLine(c.in)
		if l.Kind != LineItem {
			t.Fatalf("%q: kind=%v, want item", c.in, l.Kind)
		}
		if l.Item.Op != c.op || l.Item.Type != c.typ || l.Item.Size != c.size || l.Item.Path != c.path {
			t.Errorf("%q: got %+v", c.in, l.Item)
		}
	}
}

func TestParseProgress(t *testing.T) {
	l := ParseLine("       58527002  99%   19.38MB/s    0:00:02 (xfr#43, to-chk=1/48)")
	if l.Kind != LineProgress {
		t.Fatalf("kind=%v", l.Kind)
	}
	p := l.Progress
	if p.Bytes != 58527002 || p.Percent != 99 || !p.HasChk || p.Xfr != 43 || p.Remain != 1 || p.Total != 48 || !p.Exact {
		t.Errorf("got %+v", p)
	}
	if want := 19.38 * (1 << 20); p.SpeedBps < want-1 || p.SpeedBps > want+1 {
		t.Errorf("speed=%v want %v", p.SpeedBps, want)
	}
	if p.ETA.Seconds() != 2 {
		t.Errorf("eta=%v", p.ETA)
	}

	l = ParseLine("          32768   0%    0.00kB/s    0:00:00  ")
	if l.Kind != LineProgress || l.Progress.HasChk {
		t.Errorf("no-chk line: %+v", l)
	}
	l = ParseLine("1,238,099 100%  146.38kB/s    0:00:08  (xfr#5, ir-chk=169/396)")
	if l.Kind != LineProgress || l.Progress.Bytes != 1238099 || l.Progress.Exact {
		t.Errorf("commas/ir-chk: %+v", l.Progress)
	}
}

func TestParseOther(t *testing.T) {
	for _, s := range []string{"created directory /tmp/x", "sent 929 bytes  received 11 bytes  1880.00 bytes/sec", "::I garbage"} {
		if k := ParseLine(s).Kind; k != LineOther {
			t.Errorf("%q: kind=%v", s, k)
		}
	}
	if k := ParseLine("   ").Kind; k != LineBlank {
		t.Errorf("blank: %v", k)
	}
	l := ParseLine("Number of deleted files: 3 (reg: 2, dir: 1)")
	if l.Kind != LineStat || l.Stat.Value != 3 {
		t.Errorf("stat: %+v", l)
	}
}

func TestDecodeName(t *testing.T) {
	cases := map[string]string{
		`a\#012b`:     "a\nb",
		`plain`:       "plain",
		`trail\#01`:   `trail\#01`,
		`\#377bad`:    "�bad",
		`x\#134y`:     `x\y`,
		"raw\xffbyte": "raw�byte",
	}
	for in, want := range cases {
		if got := DecodeName(in); got != want {
			t.Errorf("DecodeName(%q)=%q want %q", in, got, want)
		}
	}
}

// fixture parcourt une sortie rsync réelle comme le fera le moteur.
func fixture(t *testing.T, ver, name string) (*Tracker, []Item, []string) {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "..", "testdata", "rsync", ver, name+".stdout"))
	if err != nil {
		t.Skipf("fixture absente: %v", err)
	}
	defer f.Close()
	tr := NewTracker()
	var items []Item
	var other []string
	sc := NewScanner(f)
	for sc.Scan() {
		l := ParseLine(sc.Text())
		tr.Apply(l)
		switch l.Kind {
		case LineItem:
			items = append(items, l.Item)
		case LineOther:
			other = append(other, l.Text)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return tr, items, other
}

func checkFullSync(t *testing.T, ver string) {
	tr, items, other := fixture(t, ver, "fullsync")
	f := tr.Final()
	if f.FilesDone != 44 || f.BytesDone != 58860002 || f.Deletes != 3 || !f.TotalsExact {
		t.Errorf("final=%+v", f)
	}
	var transfers, deletes int
	for _, it := range items {
		switch it.Op {
		case OpTransfer:
			transfers++
		case OpDelete:
			deletes++
		}
	}
	if transfers != 44 || deletes != 3 {
		t.Errorf("transfers=%d deletes=%d", transfers, deletes)
	}
	for _, o := range other {
		if strings.Contains(o, "%") && strings.Contains(o, "/s") {
			t.Errorf("progress line not recognised: %q", o)
		}
	}
}

func checkDryRun(t *testing.T, ver string) {
	tr, items, _ := fixture(t, ver, "dryrun")
	if len(items) == 0 {
		t.Fatal("no items")
	}
	found := false
	for _, it := range items {
		if it.Path == "new\nline" {
			found = true
		}
	}
	if !found {
		t.Error("escaped newline filename not decoded")
	}
	if tr.Final().Deletes != 3 {
		t.Errorf("deletes=%d", tr.Final().Deletes)
	}
}

func checkCancelPartialLine(t *testing.T, ver string) {
	tr, _, _ := fixture(t, ver, "cancel")
	p := tr.Progress()
	if p.Phase != engine.PhaseTransferring || p.BytesDone == 0 {
		t.Errorf("progress=%+v", p)
	}
}

func checkNoop(t *testing.T, ver string) {
	tr, items, _ := fixture(t, ver, "noop")
	if len(items) != 0 {
		t.Errorf("items=%v", items)
	}
	if f := tr.Final(); f.FilesDone != 0 || f.BytesDone != 0 {
		t.Errorf("final=%+v", f)
	}
}

func TestInterpretExit(t *testing.T) {
	cases := []struct {
		code             int
		cancel, vanished bool
		want             engine.Outcome
		retry            bool
	}{
		{0, false, true, engine.OutcomeSucceeded, false},
		{24, false, true, engine.OutcomeSucceeded, false},
		{24, false, false, engine.OutcomePartial, false},
		{23, false, true, engine.OutcomePartial, false},
		{25, false, true, engine.OutcomePartial, false},
		{20, true, true, engine.OutcomeCancelled, false},
		{20, false, true, engine.OutcomeFailed, false},
		{0, true, true, engine.OutcomeCancelled, false},
		{30, false, true, engine.OutcomeFailed, true},
		{255, false, true, engine.OutcomeFailed, true},
		{1, false, true, engine.OutcomeFailed, false},
		{99, false, true, engine.OutcomeFailed, false},
	}
	for _, c := range cases {
		got := InterpretExit(c.code, c.cancel, c.vanished)
		if got.Outcome != c.want || got.Retryable != c.retry {
			t.Errorf("code %d cancel=%v: got %+v", c.code, c.cancel, got)
		}
	}
}

func FuzzParseLine(f *testing.F) {
	for _, s := range []string{
		"::I >f+++++++++ 1 a", "::I *deleting   0 x/", "  1 1% 1.00kB/s 0:00:00 (xfr#1, to-chk=1/2)",
		"Number of files: 1", `::I >f+++++++++ 1 \#012`, "::I ", "::I            ",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		l := ParseLine(s)
		if l.Kind == LineItem && l.Item.Size < 0 {
			t.Errorf("negative size %q", s)
		}
	})
}

// Fixtures capturées avec plusieurs versions de rsync (spikes/rsync/capture.sh).
var fixtureVersions = []string{"3.2.7", "3.4.3", "3.5.2"}

func TestFixtures(t *testing.T) {
	for _, v := range fixtureVersions {
		t.Run(v, func(t *testing.T) {
			t.Run("fullsync", func(t *testing.T) { checkFullSync(t, v) })
			t.Run("dryrun", func(t *testing.T) { checkDryRun(t, v) })
			t.Run("cancel", func(t *testing.T) { checkCancelPartialLine(t, v) })
			t.Run("noop", func(t *testing.T) { checkNoop(t, v) })
		})
	}
}
