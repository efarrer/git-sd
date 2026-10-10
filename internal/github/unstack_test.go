package github

import "testing"

func TestUnstackTreatsAFullyMergedStackAsDone(t *testing.T) {
	withFakeGH(t, `
echo '{"message":"Pull requests #8921, #8920, #8919 cannot be removed from this stack","status":"422"}'
echo "gh: Pull requests #8921, #8920, #8919 cannot be removed from this stack (HTTP 422)" >&2
exit 1
`)
	if err := Unstack(8924); err != nil {
		t.Fatalf("a Stack whose PRs are all merged has nothing to unstack, got %v", err)
	}
}

func TestUnstackStillReportsOtherErrors(t *testing.T) {
	withFakeGH(t, `
echo '{"message":"Server Error","status":"500"}'
echo "gh: Server Error (HTTP 500)" >&2
exit 1
`)
	if err := Unstack(8924); err == nil {
		t.Fatal("any other failure must still be an error")
	}
}

func TestUnstackSucceeds(t *testing.T) {
	withFakeGH(t, `echo '{}'`)
	if err := Unstack(8924); err != nil {
		t.Fatal(err)
	}
}
