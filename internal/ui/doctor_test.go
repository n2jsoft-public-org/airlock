package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/doctor"
)

func doctorTestSections() []DoctorSection {
	return []DoctorSection{
		{Title: "First", Run: func() []doctor.Result {
			return []doctor.Result{{Status: doctor.OK, Message: "fine"}, {Status: doctor.Warning, Message: "meh", Fix: "do x"}}
		}},
		{Title: "Second", Run: func() []doctor.Result {
			return []doctor.Result{{Status: doctor.Failure, Message: "bad", Fix: "do y"}}
		}},
	}
}

func TestShowDoctorPrintsSectionsWithTheirFixesAndTheCounts(t *testing.T) {
	var out bytes.Buffer
	results, err := ShowDoctor(doctorTestSections(), strings.NewReader(""), &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[2].Message != "bad" {
		t.Fatalf("results = %+v, want the 3 results in order", results)
	}
	want := "First\n  ✓ fine\n  ! meh\n    → do x\n\nSecond\n  ✗ bad\n    → do y\n\n✓ 1  ! 1  ✗ 1 · "
	if !strings.HasPrefix(out.String(), want) {
		t.Fatalf("output = %q, want prefix %q", out.String(), want)
	}
}

func TestDoctorViewPrintsSectionsInOrder(t *testing.T) {
	sections := doctorTestSections()
	view := newDoctorView(NewStyles(true), sections)
	model, _ := view.Update(doctorSectionDoneMsg{index: 1, results: sections[1].Run()})
	view, ok := model.(doctorView)
	if !ok || view.printed != 0 {
		t.Fatalf("printed = %d after the second section, want 0 until the first answers", view.printed)
	}
	model, cmd := view.Update(doctorSectionDoneMsg{index: 0, results: sections[0].Run()})
	view, ok = model.(doctorView)
	if !ok || view.printed != 2 || !view.done() || cmd == nil {
		t.Fatalf("printed = %d, want both sections printed and the program quitting once the first answered", view.printed)
	}
}

func TestDoctorViewWrapsLongLinesUnderTheirMark(t *testing.T) {
	view := newDoctorView(NewStyles(true), nil)
	view.width = 30
	got := view.wrap("aaaa bbbb cccc dddd eeee ffff gggg", 4)
	if got != "aaaa bbbb cccc dddd eeee\n    ffff gggg" {
		t.Fatalf("wrap = %q", got)
	}
}
