package gateway

import "testing"

func TestDecide(t *testing.T) {
	cases := []struct {
		name       string
		auto       AutoConnect
		want       Verdict
		views      int
		writes     bool
		agentWrite bool
		production bool
	}{
		{name: "none prompts", auto: AutoConnectNone, views: 1, want: Prompt},
		{name: "none without view refuses", auto: AutoConnectNone, views: 0, want: Refuse},
		{name: "empty reads as none", auto: "", views: 1, want: Prompt},
		{name: "non-prod opens dev", auto: AutoConnectNonProd, views: 0, want: Auto},
		{name: "non-prod prompts prod", auto: AutoConnectNonProd, production: true, views: 1, want: Prompt},
		{name: "non-prod prod without view refuses", auto: AutoConnectNonProd, production: true, want: Refuse},
		{name: "all opens prod", auto: AutoConnectAll, production: true, want: Auto},
		{name: "writes off refuses", auto: AutoConnectAll, writes: true, views: 1, want: Refuse},
		{name: "writes always prompt", auto: AutoConnectAll, writes: true, agentWrite: true, views: 1, want: Prompt},
		{name: "writes without view refuse", auto: AutoConnectAll, writes: true, agentWrite: true, want: Refuse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.writes, &Settings{AutoConnect: tc.auto, AgentWrites: tc.agentWrite}, tc.production, tc.views)
			if got.Verdict != tc.want {
				t.Fatalf("verdict = %v (%s), want %v", got.Verdict, got.Reason, tc.want)
			}
			if (got.Verdict == Refuse) != (got.Reason != "") {
				t.Fatalf("a refusal, and only it, has a reason: %+v", got)
			}
		})
	}
	if got := Decide(true, &Settings{AutoConnect: AutoConnectAll}, false, 1); got.Reason != reasonWritesOff {
		t.Fatalf("reason = %q", got.Reason)
	}
	if got := Decide(false, &Settings{}, false, 0); got.Reason != reasonNoView {
		t.Fatalf("reason = %q", got.Reason)
	}
}
