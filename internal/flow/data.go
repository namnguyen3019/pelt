package flow

import (
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Data modes: how users get rows from the data file.
const (
	// DataUnique gives user n row n for the whole run; there must be a
	// row for every user.
	DataUnique = "unique"
	// DataSequential hands out rows in file order, one for each setup and
	// each iteration, shared across users, wrapping at the end.
	DataSequential = "sequential"
	// DataRandom picks a random row for each setup and each iteration,
	// from the user's seeded random source.
	DataRandom = "random"
)

// Data is a CSV feed: a header row of variable names and the rows below it.
type Data struct {
	Columns []string
	Rows    [][]string
	Mode    string // DataUnique, DataSequential or DataRandom

	maps []map[string]string // Rows as maps, built once; read-only
}

func readCSV(path string) (*Data, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.TrimLeadingSpace = true
	all, err := r.ReadAll() // also checks every row has the same field count
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, errors.New("file is empty; the first row must name the columns")
	}
	d := &Data{Columns: all[0], Rows: all[1:], Mode: DataUnique}
	seen := map[string]bool{}
	for i, c := range d.Columns {
		c = strings.TrimSpace(strings.TrimPrefix(c, "\ufeff")) // tolerate a BOM
		d.Columns[i] = c
		if !varName.MatchString(c) {
			return nil, fmt.Errorf("column %q is not a valid variable name", c)
		}
		if seen[c] {
			return nil, fmt.Errorf("column %q appears twice", c)
		}
		seen[c] = true
	}
	if len(d.Rows) == 0 {
		return nil, errors.New("no data rows below the header")
	}
	d.maps = make([]map[string]string, len(d.Rows))
	for i, rec := range d.Rows {
		m := make(map[string]string, len(d.Columns))
		for j, c := range d.Columns {
			m[c] = rec[j]
		}
		d.maps[i] = m
	}
	return d, nil
}

// row returns row i (0-based, wrapping) as a map. Callers must not change
// it: every user shares the same maps.
func (d *Data) row(i int) map[string]string {
	if d == nil {
		return nil
	}
	return d.maps[i%len(d.maps)]
}

// drawRow gives the user its row for the setup or iteration about to
// start. In unique mode the row never changes.
func (v *vu) drawRow() {
	d := v.sc.Data
	if d == nil {
		return
	}
	switch d.Mode {
	case DataSequential:
		v.row = d.row(int(v.r.dataNext.Add(1)-1) % len(d.maps))
	case DataRandom:
		v.row = d.row(v.rng.IntN(len(d.maps)))
	default:
		v.row = d.row(v.id - 1)
	}
}
