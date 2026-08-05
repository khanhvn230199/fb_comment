package scraper

import "testing"

func TestParseSocialCount(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int64
	}{
		{name: "plain vietnamese comments", value: "886 bình luận", want: 886},
		{name: "vietnamese comments must not treat b as billion", value: "886 bình luận trên bài viết", want: 886},
		{name: "plain english likes", value: "11 likes", want: 11},
		{name: "compact k suffix", value: "1,2K lượt thích", want: 1200},
		{name: "vietnamese thousand suffix", value: "1,2 nghìn bình luận", want: 1200},
		{name: "vietnamese million suffix", value: "2 triệu lượt thích", want: 2000000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseSocialCount(tt.value)
			if !ok {
				t.Fatalf("parseSocialCount(%q) did not match", tt.value)
			}
			if got != tt.want {
				t.Fatalf("parseSocialCount(%q) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}
