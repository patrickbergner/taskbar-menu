//go:build windows

package main

import "testing"

// columnsOf replays what fillMenu does with a break slice: it reports how tall
// each resulting column is, so a test can assert on the shape of the layout
// rather than on which indices happened to be marked.
func columnsOf(heights []int32, breaks []bool) []int32 {
	cols := []int32{0}
	for i, h := range heights {
		if breaks != nil && breaks[i] {
			cols = append(cols, 0)
		}
		cols[len(cols)-1] += h
	}
	return cols
}

func repeat(n int, h int32) []int32 {
	out := make([]int32, n)
	for i := range out {
		out[i] = h
	}
	return out
}

// A menu that fits gets no breaks at all, which is both the common case and the
// one where a stray break would be most visible.
func TestSplitColumnsFits(t *testing.T) {
	cases := []struct {
		name    string
		heights []int32
		availH  int32
	}{
		{"well under", repeat(10, 32), 1000},
		{"exactly full", repeat(10, 32), 320},
		{"one entry", repeat(1, 32), 10}, // nothing to split it against
		{"empty", nil, 1000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := splitColumns(c.heights, 200, c.availH, 1920); got != nil {
				t.Errorf("splitColumns() = %v, want nil", got)
			}
		})
	}
}

// The whole point of the feature: no column may exceed the height that made
// Windows show its scroll arrows in the first place, and the columns together
// may not be wider than the screen. Windows enforces neither -- it sizes a
// multi-column popup to whatever the columns ask for and lets it hang off the
// edge -- so every split this returns has to satisfy both on its own.
func TestSplitColumnsEveryColumnFits(t *testing.T) {
	const availH, availW, itemH, colW = 1000, 1920, 32, 200

	for _, n := range []int{32, 33, 60, 63, 100, 180} {
		heights := repeat(n, itemH)
		breaks := splitColumns(heights, colW, availH, availW)
		cols := columnsOf(heights, breaks)

		if int32(len(cols))*colW > availW {
			t.Errorf("%d entries: %d columns is %dpx, wider than the %dpx screen",
				n, len(cols), int32(len(cols))*colW, availW)
		}
		for i, h := range cols {
			if h > availH {
				t.Errorf("%d entries: column %d is %dpx, over the %dpx budget",
					n, i, h, availH)
			}
		}
	}
}

// Columns should come out near-equal rather than filled to the brim and then
// trailed by a stub.
func TestSplitColumnsBalanced(t *testing.T) {
	heights := repeat(33, 32) // one entry past a single column
	breaks := splitColumns(heights, 200, 1024, 1920)
	cols := columnsOf(heights, breaks)

	if len(cols) != 2 {
		t.Fatalf("got %d columns, want 2", len(cols))
	}
	if diff := cols[0] - cols[1]; diff > 32 || diff < -32 {
		t.Errorf("columns %v differ by more than one entry", cols)
	}
}

// Nothing is ever dropped or duplicated by the split, whatever the shape of the
// input: fillMenu walks entries and breaks in lockstep, so a short slice would
// panic and a long one would silently mislabel the tail.
func TestSplitColumnsPreservesEntries(t *testing.T) {
	heights := []int32{32, 7, 32, 32, 48, 32, 7, 32, 32, 32, 32, 32}
	breaks := splitColumns(heights, 200, 100, 1920)

	if len(breaks) != len(heights) {
		t.Fatalf("got %d breaks for %d entries", len(breaks), len(heights))
	}
	if breaks[0] {
		t.Error("first entry marked as starting a new column")
	}

	var total int32
	for _, h := range columnsOf(heights, breaks) {
		total += h
	}
	var want int32
	for _, h := range heights {
		want += h
	}
	if total != want {
		t.Errorf("columns total %dpx, entries total %dpx", total, want)
	}
}

// A menu too big for even a screen full of columns is left unsplit, so Windows
// scrolls one column as it did before the feature existed. Filling the screen
// and spilling the rest off the edge is the one outcome that must not happen:
// Windows scrolls a single column but not a multi-column popup, so those
// entries would be reachable by nothing at all.
func TestSplitColumnsTooBigToHelp(t *testing.T) {
	const availH, availW, colW = 1000, 1920, 300 // 6 columns of 31 entries fit

	if got := splitColumns(repeat(500, 32), colW, availH, availW); got != nil {
		t.Errorf("500 entries: got a %d-column split, want nil", len(columnsOf(repeat(500, 32), got)))
	}
	// One entry past what the grid holds is still one entry too many.
	if got := splitColumns(repeat(6*31+1, 32), colW, availH, availW); got != nil {
		t.Errorf("187 entries: got columns %v, want nil", columnsOf(repeat(6*31+1, 32), got))
	}
	// And the largest menu that does fit still splits, so the giving-up above is
	// not passing for want of any split at all.
	if got := splitColumns(repeat(6*31, 32), colW, availH, availW); got == nil {
		t.Error("186 entries: got nil, want a split")
	}
}

// The invariant, swept across sizes rather than spot-checked: whenever a split
// comes back, every column is within budget and the columns fit the screen.
// Both are on us -- Windows enforces neither -- and the failure that motivated
// this sweep only showed up at one entry count in a hundred, where an even
// division rounded a single column one entry over.
func TestSplitColumnsInvariant(t *testing.T) {
	const availH, availW, colW = 1000, 1920, 300

	// Uneven entry heights, since equal ones hide exactly the rounding this is
	// looking for: separators and spacers are much shorter than real entries.
	shapes := map[string]func(i int) int32{
		"uniform": func(int) int32 { return 32 },
		"separated": func(i int) int32 {
			if i%12 == 0 {
				return 7
			}
			return 32
		},
		"mixed": func(i int) int32 { return int32(24 + i%17) },
	}

	for name, shape := range shapes {
		t.Run(name, func(t *testing.T) {
			for n := 2; n <= 400; n++ {
				heights := make([]int32, n)
				for i := range heights {
					heights[i] = shape(i)
				}
				breaks := splitColumns(heights, colW, availH, availW)
				if breaks == nil {
					continue // fell back to Windows' scrolling; nothing to check
				}
				cols := columnsOf(heights, breaks)
				if int32(len(cols))*colW > availW {
					t.Fatalf("%d entries: %d columns is %dpx, wider than the %dpx screen",
						n, len(cols), int32(len(cols))*colW, availW)
				}
				for c, h := range cols {
					if h > availH {
						t.Fatalf("%d entries: column %d of %d is %dpx, over the %dpx budget",
							n, c, len(cols), h, availH)
					}
				}
			}
		})
	}
}

// An unknown work area (no monitor identified; see resolvePlacement) has to
// leave the layout alone rather than divide by it.
func TestSplitColumnsUnknownWorkArea(t *testing.T) {
	if got := splitColumns(repeat(50, 32), 0, 0, 0); got != nil {
		t.Errorf("splitColumns() = %v, want nil", got)
	}
	// A widest of zero -- every entry a separator -- must not divide by it.
	if got := splitColumns(repeat(50, 32), 0, 1000, 1920); got == nil {
		t.Error("splitColumns() = nil, want a split")
	}
}
