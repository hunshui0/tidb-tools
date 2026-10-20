package dbutil

import "github.com/pingcap/tidb/pkg/parser/charset"

func init() {
	if _, err := charset.GetCharsetInfo(charset.CharsetGB18030); err == nil {
		return
	}

	// The pinned TiDB parser knows GB18030 collation IDs, but omits the charset
	// from its enabled registry. Register metadata before any parser goroutines
	// start; AddCharset mutates a global map and is not safe during SQL parsing.
	// This preserves SHOW CREATE TABLE names instead of rewriting them to UTF-8.
	// It does not install a local GB18030 collation comparison implementation.
	collations := make(map[string]*charset.Collation)
	for _, name := range []string{"gb18030_bin", "gb18030_chinese_ci", "gb18030_unicode_520_ci"} {
		if collation, err := charset.GetCollationByName(name); err == nil {
			collations[name] = collation
		}
	}
	charset.AddCharset(&charset.Charset{
		Name:             charset.CharsetGB18030,
		DefaultCollation: "gb18030_bin",
		Collations:       collations,
		Desc:             "China National Standard GB18030",
		Maxlen:           4,
	})
}
