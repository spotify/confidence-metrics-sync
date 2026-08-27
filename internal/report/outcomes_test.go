package report

import (
	"strings"
	"testing"
)

// An adoption names the source it takes the resource from — and degrades to
// the old wording rather than a dangling "from" if the server sends none,
// which is what a CLI running ahead of the server would see.
func TestAdoptLineNamesThePreviousSource(t *testing.T) {
	var b strings.Builder
	Outcomes(&b, []OutcomeItem{
		{Kind: "metric", DisplayName: "Retention", Action: "ADOPT",
			ChangedFields: []string{"source"}, PreviousReference: "github.com/org/repo-a"},
		{Kind: "metric", DisplayName: "Streams", Action: "ADOPT", ChangedFields: []string{"source"}},
	}, true)

	out := b.String()
	if !strings.Contains(out, `» metric "Retention" from github.com/org/repo-a (source)`) {
		t.Errorf("adoption did not name its source:\n%s", out)
	}
	if !strings.Contains(out, `» metric "Streams" (source)`) {
		t.Errorf("adoption without a source should not render a dangling from:\n%s", out)
	}
}

func TestRenameHintOnSameKindCreateArchivePair(t *testing.T) {
	var b strings.Builder
	Outcomes(&b, []OutcomeItem{
		{Kind: "metric", DisplayName: "Minutes Played v2", Action: "CREATE"},
		{Kind: "metric", DisplayName: "Minutes Played", Action: "ARCHIVE"},
	}, true)

	out := b.String()
	for _, s := range []string{
		"possible resource-name change",
		`creates metric "Minutes Played v2"`,
		`archives "Minutes Played"`,
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q:\n%s", s, out)
		}
	}
}

func TestNoRenameHintAcrossKinds(t *testing.T) {
	var b strings.Builder
	// A created fact table and an archived metric are unrelated changes,
	// not a rename candidate.
	Outcomes(&b, []OutcomeItem{
		{Kind: "fact table", DisplayName: "Streams", Action: "CREATE"},
		{Kind: "metric", DisplayName: "Old Metric", Action: "ARCHIVE"},
	}, true)

	if strings.Contains(b.String(), "possible resource-name change") {
		t.Errorf("cross-kind create+archive must not hint a rename:\n%s", b.String())
	}
}

func TestNoRenameHintWithoutArchive(t *testing.T) {
	var b strings.Builder
	Outcomes(&b, []OutcomeItem{
		{Kind: "metric", DisplayName: "New Metric", Action: "CREATE"},
		{Kind: "metric", DisplayName: "Existing", Action: "UNCHANGED"},
	}, false)

	if strings.Contains(b.String(), "possible resource-name change") {
		t.Errorf("create without archive must not hint a rename:\n%s", b.String())
	}
}
