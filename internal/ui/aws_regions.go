package ui

import (
	"regexp"
	"strings"
)

// awsRegion is one entry of the region switcher.
type awsRegion struct {
	code string
	name string
	// keywords are extra search terms (country, ASCII spelling) not present in the name.
	keywords string
}

// awsRegions lists the AWS commercial regions offered by the region switcher, Europe first.
var awsRegions = []awsRegion{
	{code: "eu-west-3", name: "Europe (Paris)", keywords: "france"},
	{code: "eu-west-1", name: "Europe (Ireland)", keywords: "dublin"},
	{code: "eu-west-2", name: "Europe (London)", keywords: "uk united kingdom england"},
	{code: "eu-central-1", name: "Europe (Frankfurt)", keywords: "germany"},
	{code: "eu-central-2", name: "Europe (Zurich)", keywords: "switzerland"},
	{code: "eu-south-1", name: "Europe (Milan)", keywords: "italy"},
	{code: "eu-south-2", name: "Europe (Spain)", keywords: "aragon"},
	{code: "eu-north-1", name: "Europe (Stockholm)", keywords: "sweden"},
	{code: "us-east-1", name: "US East (N. Virginia)", keywords: "usa virginia"},
	{code: "us-east-2", name: "US East (Ohio)", keywords: "usa"},
	{code: "us-west-1", name: "US West (N. California)", keywords: "usa california"},
	{code: "us-west-2", name: "US West (Oregon)", keywords: "usa"},
	{code: "ca-central-1", name: "Canada (Central)", keywords: "montreal"},
	{code: "ca-west-1", name: "Canada West (Calgary)"},
	{code: "mx-central-1", name: "Mexico (Central)"},
	{code: "sa-east-1", name: "South America (São Paulo)", keywords: "sao paulo brazil"},
	{code: "ap-northeast-1", name: "Asia Pacific (Tokyo)", keywords: "japan"},
	{code: "ap-northeast-2", name: "Asia Pacific (Seoul)", keywords: "korea"},
	{code: "ap-northeast-3", name: "Asia Pacific (Osaka)", keywords: "japan"},
	{code: "ap-east-1", name: "Asia Pacific (Hong Kong)", keywords: "china"},
	{code: "ap-east-2", name: "Asia Pacific (Taipei)", keywords: "taiwan"},
	{code: "ap-south-1", name: "Asia Pacific (Mumbai)", keywords: "india"},
	{code: "ap-south-2", name: "Asia Pacific (Hyderabad)", keywords: "india"},
	{code: "ap-southeast-1", name: "Asia Pacific (Singapore)"},
	{code: "ap-southeast-2", name: "Asia Pacific (Sydney)", keywords: "australia"},
	{code: "ap-southeast-3", name: "Asia Pacific (Jakarta)", keywords: "indonesia"},
	{code: "ap-southeast-4", name: "Asia Pacific (Melbourne)", keywords: "australia"},
	{code: "ap-southeast-5", name: "Asia Pacific (Malaysia)", keywords: "kuala lumpur"},
	{code: "ap-southeast-6", name: "Asia Pacific (New Zealand)", keywords: "auckland"},
	{code: "ap-southeast-7", name: "Asia Pacific (Thailand)", keywords: "bangkok"},
	{code: "me-central-1", name: "Middle East (UAE)", keywords: "emirates"},
	{code: "me-south-1", name: "Middle East (Bahrain)"},
	{code: "il-central-1", name: "Israel (Tel Aviv)"},
	{code: "af-south-1", name: "Africa (Cape Town)", keywords: "south africa"},
}

var regionCodePattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d+$`)

func (r awsRegion) searchText() string {
	return strings.ToLower(r.code + " " + r.name + " " + r.keywords)
}

// filterAWSRegions returns the regions matching every word of query in their code, name or keywords.
func filterAWSRegions(regions []awsRegion, query string) []awsRegion {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return append([]awsRegion(nil), regions...)
	}

	matches := make([]awsRegion, 0, len(regions))
	for _, region := range regions {
		text := region.searchText()
		matched := true
		for _, word := range words {
			if !strings.Contains(text, word) {
				matched = false
				break
			}
		}
		if matched {
			matches = append(matches, region)
		}
	}
	return matches
}

// awsRegionName returns the display name of code, or "" for regions missing from the list.
func awsRegionName(code string) string {
	for _, region := range awsRegions {
		if region.code == code {
			return region.name
		}
	}
	return ""
}

// looksLikeRegionCode reports whether value has the shape of a region code such as "eu-west-3".
func looksLikeRegionCode(value string) bool {
	return regionCodePattern.MatchString(value)
}
