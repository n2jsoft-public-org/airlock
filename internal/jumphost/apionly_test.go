package jumphost

import "testing"

func TestAPIOnlyTypes(t *testing.T) {
	for _, c := range []struct {
		resourceType string
		want         bool
	}{{"dynamodb", true}, {" S3 ", true}, {"rds", false}, {"direct", false}, {"docker", false}, {"", false}} {
		if got := APIOnly(c.resourceType); got != c.want {
			t.Fatalf("APIOnly(%q) = %v, want %v", c.resourceType, got, c.want)
		}
	}
}
