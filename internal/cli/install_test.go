package cli

import (
	"reflect"
	"testing"

	"github.com/ansibleforms/installer/internal/ui"
)

// TestVisibleLabelsKeepsAnsweredLanguage checks that the language, asked before the wizard
// began (so its own step is skipped), stays at the left, while a step skipped for another
// reason does not show.
func TestVisibleLabelsKeepsAnsweredLanguage(t *testing.T) {
	asked := true
	skipped := func() bool { return true }
	list := []wstep{
		{label: "language", skip: skipped, shown: func() bool { return asked }},
		{label: "notes"},
		{label: "folder", skip: skipped},
		{label: "role"},
	}
	want := []string{stepName("language"), stepName("notes"), stepName("role")}
	if got := visibleLabels(list); !reflect.DeepEqual(got, want) {
		t.Fatalf("visibleLabels = %v, want %v", got, want)
	}
	// given with --lang : never asked, so not at the left
	asked = false
	want = []string{stepName("notes"), stepName("role")}
	if got := visibleLabels(list); !reflect.DeepEqual(got, want) {
		t.Fatalf("visibleLabels with --lang = %v, want %v", got, want)
	}
}

// TestRunWizardBackToAnsweredLanguage checks that Esc on the step after the language, asked
// before the wizard began, asks the language again, then goes on.
func TestRunWizardBackToAnsweredLanguage(t *testing.T) {
	var asked []string
	notesSeen := 0
	list := []wstep{
		{label: "language", skip: func() bool { return true }, shown: func() bool { return true }, ask: func() error {
			asked = append(asked, "language")
			return nil
		}},
		{label: "notes", ask: func() error {
			asked = append(asked, "notes")
			notesSeen++
			if notesSeen == 1 {
				return ui.ErrBack
			}
			return nil
		}},
		{label: "role", ask: func() error {
			asked = append(asked, "role")
			return nil
		}},
	}
	if err := runWizard(list); err != nil {
		t.Fatal(err)
	}
	want := []string{"notes", "language", "notes", "role"}
	if !reflect.DeepEqual(asked, want) {
		t.Fatalf("asked %v, want %v", asked, want)
	}
}
