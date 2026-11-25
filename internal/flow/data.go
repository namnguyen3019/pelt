package flow

import (
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Data is a CSV feed: a header row of variable names and the rows below it.
// In "unique" mode, VU n (1-based) gets row n.
type Data struct {
	Columns []string
	Rows    [][]string
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
	d := &Data{Columns: all[0], Rows: all[1:]}
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
	return d, nil
}

// row returns VU vu's values (vu is 1-based) as a map.
func (d *Data) row(vu int) map[string]string {
	if d == nil {
		return nil
	}
	rec := d.Rows[(vu-1)%len(d.Rows)]
	m := make(map[string]string, len(d.Columns))
	for i, c := range d.Columns {
		m[c] = rec[i]
	}
	return m
}
