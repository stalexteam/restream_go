package probe

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"restream_go/internal/wire/ts"
)

var tsFixtures = []string{"single", "ladder", "edge"}

type goldenManifestDoc struct {
	Manifest         ts.Manifest `json:"manifest"`
	GeometryComplete bool        `json:"geometry_complete"`
}

func loadGoldenManifest(t *testing.T, name string) goldenManifestDoc {
	t.Helper()
	path := filepath.Join("..", "wire", "ts", "testdata", name+".manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("missing %s (run DEV/tools/gen_ts_golden.py in the wire/ts package): %v", path, err)
	}
	var doc goldenManifestDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("bad %s: %v", path, err)
	}
	return doc
}

func decompressToTemp(t *testing.T, gzPath string) string {
	t.Helper()
	f, err := os.Open(gzPath)
	if err != nil {
		t.Skipf("missing fixture %s: %v", gzPath, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "fixture.ts")
	w, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := io.Copy(w, gz); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestReadManifestAgainstOracle звіряє readManifest (без спавну транспорту) з
// wire/ts golden: та сама конфігурація TsDemuxer, що всередині probe_ts_manifest

func TestReadManifestAgainstOracle(t *testing.T) {
	for _, name := range tsFixtures {
		t.Run(name, func(t *testing.T) {
			golden := loadGoldenManifest(t, name)
			path := decompressToTemp(t, filepath.Join("..", "wire", "ts", "testdata", name+".ts.gz"))
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()

			demuxer, gotData, readErr := readManifest(f, DefaultManifestTimeoutSec, DefaultManifestMinWindowSec)
			if readErr != nil {
				t.Fatalf("readManifest: %v", readErr)
			}
			if !gotData {
				t.Fatal("readManifest: no data read from fixture")
			}
			demuxer.Flush()

			if got := demuxer.Manifest(); !reflect.DeepEqual(got, golden.Manifest) {
				t.Fatalf("manifest mismatch:\ngot  %+v\nwant %+v", got, golden.Manifest)
			}
			if got := demuxer.VideoGeometryComplete(); got != golden.GeometryComplete {
				t.Fatalf("geometry_complete: got %v, want %v", got, golden.GeometryComplete)
			}
			t.Logf("%s: manifest matches oracle golden (video=%d audio=%d)",
				name, len(golden.Manifest.Video), golden.Manifest.Audio)
		})
	}
}

// TestProbeTSManifestRejectsNonSRT — probe_ts_manifest:163-164 (url validation,
// не потребує транспорту).
func TestProbeTSManifestRejectsNonSRT(t *testing.T) {
	cases := []string{"rtmp://127.0.0.1/live/foo", "http://example.com/x", ""}
	for _, url := range cases {
		if _, ok := ProbeTSManifest(url, 1, 0.1); ok {
			t.Errorf("ProbeTSManifest(%q) = ok, want rejected (not srt://)", url)
		}
	}
}
