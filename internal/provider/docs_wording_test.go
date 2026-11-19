package provider

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The generated docs are the Terraform Registry pages and are copied into
// docs.kupe.cloud, so they describe what the user sees and never name the
// platform's internals (docs-public#141). The ha_etcd_* attribute names are
// part of the provider's contract and stay; the prose around them does not
// say etcd.
func TestDocs_PublicWordingNamesNoInternals(t *testing.T) {
	denied := regexp.MustCompile(`(?i)\betcd\b|quorum|anti-affinity|soft-delet|kv v2|openbao|vault refuses|entry's metadata`)
	attribute := regexp.MustCompile(`(?i)ha_etcd_[a-z_]+`)
	err := filepath.WalkDir("../../docs", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		data, err := os.ReadFile(path) // #nosec G304 -- walks the repo's own docs tree
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if hit := denied.FindString(attribute.ReplaceAllString(line, "")); hit != "" {
				t.Errorf("%s:%d names %q; describe it generically and run make docs", path, i+1, hit)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
