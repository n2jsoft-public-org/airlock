package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func regionCodes(regions []awsRegion) []string {
	codes := make([]string, 0, len(regions))
	for _, region := range regions {
		codes = append(codes, region.code)
	}
	return codes
}

func TestFilterAWSRegionsMatchesCodeNameAndKeywords(t *testing.T) {
	cases := map[string]string{
		"paris":        "eu-west-3",
		"EU-WEST-3":    "eu-west-3",
		"tokyo":        "ap-northeast-1",
		"sao paulo":    "sa-east-1",
		"germany":      "eu-central-1",
		"europe milan": "eu-south-1",
	}
	for query, want := range cases {
		got := regionCodes(filterAWSRegions(awsRegions, query))
		if len(got) != 1 || got[0] != want {
			t.Errorf("filter %q = %v, want [%s]", query, got, want)
		}
	}

	if got := filterAWSRegions(awsRegions, "  "); len(got) != len(awsRegions) {
		t.Errorf("empty query returned %d regions, want all %d", len(got), len(awsRegions))
	}
	if got := filterAWSRegions(awsRegions, "eu-west"); len(got) != 3 {
		t.Errorf("eu-west matched %v, want the 3 eu-west regions", regionCodes(got))
	}
}

func TestAWSRegionCodesAreUniqueAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, region := range awsRegions {
		if seen[region.code] {
			t.Errorf("duplicate region %s", region.code)
		}
		seen[region.code] = true
		if !looksLikeRegionCode(region.code) {
			t.Errorf("region code %q is malformed", region.code)
		}
	}
}

func pickerUpdate(t *testing.T, p regionPicker, msg tea.Msg) (regionPicker, regionPickerOutcome) {
	t.Helper()
	p, outcome, _ := p.Update(msg)
	return p, outcome
}

func TestRegionPickerStartsOnCurrentRegion(t *testing.T) {
	p := newRegionPicker("us-east-1")
	if code, _ := p.choice(); code != "us-east-1" {
		t.Fatalf("highlighted = %s, want the current region", code)
	}
}

func TestRegionPickerSearchesAndChooses(t *testing.T) {
	p := newRegionPicker("eu-west-3")

	p, _ = pickerUpdate(t, p, key("tokyo"))
	p, outcome := pickerUpdate(t, p, key("enter"))
	if outcome != regionPickerChosen {
		t.Fatalf("outcome = %v, want chosen", outcome)
	}
	if code, _ := p.choice(); code != "ap-northeast-1" {
		t.Fatalf("choice = %s, want ap-northeast-1", code)
	}
}

func TestRegionPickerMovesWithinMatches(t *testing.T) {
	p := newRegionPicker("")
	p, _ = pickerUpdate(t, p, key("eu-west"))
	p, _ = pickerUpdate(t, p, key("down"))
	p, _ = pickerUpdate(t, p, key("down"))
	p, _ = pickerUpdate(t, p, key("down"))
	if code, _ := p.choice(); code != "eu-west-2" {
		t.Fatalf("choice = %s, want the last eu-west match eu-west-2", code)
	}
}

func TestRegionPickerAcceptsUnlistedRegionCode(t *testing.T) {
	p := newRegionPicker("")
	p, _ = pickerUpdate(t, p, key("eu-east-9"))
	if _, outcome := pickerUpdate(t, p, key("enter")); outcome != regionPickerChosen {
		t.Fatal("a well-formed region code missing from the list should be accepted")
	}

	p = newRegionPicker("")
	p, _ = pickerUpdate(t, p, key("atlantis"))
	if _, outcome := pickerUpdate(t, p, key("enter")); outcome != regionPickerOpen {
		t.Fatal("enter without any match must keep the picker open")
	}
}

func TestRegionPickerEscCancels(t *testing.T) {
	if _, outcome := pickerUpdate(t, newRegionPicker(""), key("esc")); outcome != regionPickerCanceled {
		t.Fatalf("outcome = %v, want canceled", outcome)
	}
}
